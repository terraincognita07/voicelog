package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/terraincognita07/voicelog/internal/audio"
	"github.com/terraincognita07/voicelog/internal/db"
	"github.com/terraincognita07/voicelog/internal/promptbuilder"
	"github.com/terraincognita07/voicelog/internal/whisper"
)

// RetranscribeDeps bundles the extra dependencies needed for the
// retranscribe tool. nil-or-zero means the tool is registered but will
// return an "audio retention disabled" error to the caller — the rest
// of the MCP surface works regardless.
//
// AudioDir is where the bot stores retained audio files. Used to
// resolve relative audio_path values (the post-F3 format) before
// handing the path to whisper. Empty string means MCP can only reach
// notes that still hold legacy absolute paths.
type RetranscribeDeps struct {
	Whisper             *whisper.Client
	BasePrompt          string
	HallucinationThresh float64
	AudioDir            string
}

// retranscribeResponse is what Claude gets back from a successful
// retranscribe call. old_text / new_text let it summarize the diff;
// confidence_overall surfaces whether the new run was actually better.
type retranscribeResponse struct {
	NoteID  int64  `json:"note_id"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
	// Always present per docs/MCP.md (null when the new run had no segments);
	// no omitempty so the documented 6-field result shape is stable.
	ConfidenceOverall    *float64 `json:"confidence_overall"`
	ConfidenceMin        *float64 `json:"confidence_min"`
	SuspectHallucination bool     `json:"suspect_hallucination"`
}

func registerRetranscribe(s *server.MCPServer, store *db.DB, deps RetranscribeDeps, logger *slog.Logger) {
	tool := mcpsdk.NewTool("retranscribe",
		mcpsdk.WithDescription("Re-run whisper on a note's retained audio file. Requires the note "+
			"to have audio_path set (AUDIO_RETENTION_DAYS must be > 0 on the bot side, AND the "+
			"note must be young enough that the janitor hasn't deleted its audio). The previous "+
			"raw_text is archived into notes_history before the note is updated, so the change "+
			"is reversible at the SQL level. Returns old_text + new_text + the new confidence "+
			"metrics so the caller can summarize the diff."),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(false),
		mcpsdk.WithOpenWorldHintAnnotation(true), // depends on external whisper
		mcpsdk.WithNumber("id", mcpsdk.Required(),
			mcpsdk.Description("Note ID to re-transcribe."),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		if deps.Whisper == nil {
			return mcpsdk.NewToolResultError("retranscribe is unavailable — the MCP server was started without a whisper client"), nil
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()

		idF, err := req.RequireFloat("id")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		id := int64(idF)

		note, err := store.GetNote(ctx, id)
		if err != nil {
			if errors.Is(err, db.ErrNoteNotFound) {
				return mcpsdk.NewToolResultError(fmt.Sprintf("note %d not found", id)), nil
			}
			logger.Error("retranscribe: get note", "id", id, "err", err)
			return mcpsdk.NewToolResultError("could not load note"), nil
		}
		if !note.AudioPath.Valid || note.AudioPath.String == "" {
			return mcpsdk.NewToolResultError(fmt.Sprintf("note %d has no retained audio — enable AUDIO_RETENTION_DAYS or pick a younger note", id)), nil
		}

		audioPath := audio.Resolve(deps.AudioDir, note.AudioPath.String)
		prompt := promptbuilder.Compose(ctx, store, deps.BasePrompt, logger)
		result, err := deps.Whisper.Transcribe(ctx, audioPath, prompt)
		if err != nil {
			logger.Error("retranscribe: whisper", "id", id, "err", err)
			return mcpsdk.NewToolResultError("whisper failed — see server logs"), nil
		}
		newText := strings.TrimSpace(result.Text)
		if newText == "" {
			return mcpsdk.NewToolResultError("whisper returned empty text — refusing to overwrite the note"), nil
		}

		meta := db.NoteMeta{}
		overall, worst, suspect, ok := result.Aggregate(deps.HallucinationThresh)
		if ok {
			meta.ConfidenceOverall = &overall
			meta.ConfidenceMin = &worst
			meta.SuspectHallucination = suspect
		}

		oldText, err := store.ArchiveAndUpdateText(ctx, id, newText, "", meta)
		if err != nil {
			if errors.Is(err, db.ErrNoteNotFound) {
				return mcpsdk.NewToolResultError(fmt.Sprintf("note %d disappeared mid-retranscribe", id)), nil
			}
			logger.Error("retranscribe: archive+update", "id", id, "err", err)
			return mcpsdk.NewToolResultError("could not persist new transcription"), nil
		}

		resp := retranscribeResponse{
			NoteID:               id,
			OldText:              oldText,
			NewText:              newText,
			SuspectHallucination: suspect,
		}
		if ok {
			resp.ConfidenceOverall = &overall
			resp.ConfidenceMin = &worst
		}
		return jsonResult(resp)
	})
}
