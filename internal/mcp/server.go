package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"voicelog/internal/audio"
	"voicelog/internal/db"
	"voicelog/internal/whisper"
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

const (
	serverName    = "voicelog"
	serverVersion = "0.1.0"
)

type mcpNote struct {
	ID                   int64    `json:"id"`
	CreatedAtISO         string   `json:"created_at_iso"`
	RawText              string   `json:"raw_text"`
	DurationSec          int64    `json:"duration_sec"`
	Status               string   `json:"status,omitempty"`
	Rank                 float64  `json:"rank,omitempty"`
	Snippet              string   `json:"snippet,omitempty"`
	ConfidenceOverall    *float64 `json:"confidence_overall,omitempty"` // mean avg_logprob; nil = unknown
	ConfidenceMin        *float64 `json:"confidence_min,omitempty"`     // worst segment avg_logprob
	SuspectHallucination bool     `json:"suspect_hallucination,omitempty"`
}

func toMCP(n db.Note) mcpNote {
	out := mcpNote{
		ID:                   n.ID,
		CreatedAtISO:         n.CreatedAt.UTC().Format(time.RFC3339),
		RawText:              n.RawText,
		DurationSec:          n.DurationSec.Int64,
		Status:               string(n.Status),
		SuspectHallucination: n.SuspectHallucination,
	}
	if n.ConfidenceOverall.Valid {
		v := n.ConfidenceOverall.Float64
		out.ConfidenceOverall = &v
	}
	if n.ConfidenceMin.Valid {
		v := n.ConfidenceMin.Float64
		out.ConfidenceMin = &v
	}
	return out
}

// NewServer builds the MCP server and registers every voicelog tool.
// retranscribe is registered unconditionally; if RetranscribeDeps.Whisper
// is nil (i.e. the operator didn't wire whisper into mcp), calls return
// a clear error rather than panicking.
func NewServer(store *db.DB, deps RetranscribeDeps, logger *slog.Logger) *server.MCPServer {
	s := server.NewMCPServer(serverName, serverVersion,
		server.WithToolCapabilities(true),
	)

	registerListPending(s, store, logger)
	registerGetRange(s, store, logger)
	registerSearch(s, store, logger)
	registerMarkAnalyzed(s, store, logger)
	registerGetNote(s, store, logger)
	registerDiscardNotes(s, store, logger)
	registerRestoreNote(s, store, logger)
	registerRetranscribe(s, store, deps, logger)
	registerDBHealth(s, store, logger)

	return s
}

func registerDBHealth(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("db_health",
		mcpsdk.WithDescription("Run SQLite's integrity_check + quick_check, then report note count and "+
			"on-disk size. integrity_check / quick_check return the literal string \"ok\" on a healthy "+
			"DB; anything else is a real corruption signal. Cheap on a small DB — safe to call ad-hoc "+
			"(say, weekly) to verify the backup is still readable."),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		rep, err := store.Health(ctx)
		if err != nil {
			logger.Error("db_health", "err", err)
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		return jsonResult(rep)
	})
}

func registerListPending(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("list_pending_notes",
		mcpsdk.WithDescription("List the most recent notes with status='pending'. Newest first."),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithNumber("limit",
			mcpsdk.Description("Maximum number of notes to return. Default 50."),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		limit := 50
		if v, err := req.RequireFloat("limit"); err == nil && v > 0 {
			limit = int(v)
		}
		notes, err := store.ListPending(ctx, limit)
		if err != nil {
			logger.Error("list_pending_notes", "err", err)
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		out := make([]mcpNote, len(notes))
		for i, n := range notes {
			m := toMCP(n)
			m.Status = "" // implied by tool name
			out[i] = m
		}
		return jsonResult(out)
	})
}

func registerGetRange(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("get_notes_in_range",
		mcpsdk.WithDescription("List notes whose created_at falls in [from, to). "+
			"Both bounds are ISO8601 (e.g. 2026-05-26T00:00:00Z). "+
			"Discarded notes are excluded by default — they represent the user's "+
			"'forget this' signal. Set include_discarded=true or status='discarded' "+
			"if you specifically need them."),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithString("from", mcpsdk.Required(),
			mcpsdk.Description("Inclusive lower bound, ISO8601."),
		),
		mcpsdk.WithString("to", mcpsdk.Required(),
			mcpsdk.Description("Exclusive upper bound, ISO8601."),
		),
		mcpsdk.WithString("status",
			mcpsdk.Description("Optional explicit status filter. When set, overrides include_discarded."),
			mcpsdk.Enum("pending", "analyzed", "discarded"),
		),
		mcpsdk.WithBoolean("include_discarded",
			mcpsdk.Description("If true, do not auto-filter discarded notes. Ignored when 'status' is set. Default false."),
		),
		mcpsdk.WithNumber("limit",
			mcpsdk.Description(fmt.Sprintf("Maximum notes to return. Default and hard cap %d.", db.MaxNotesInRange)),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		fromStr, err := req.RequireString("from")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		toStr, err := req.RequireString("to")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		from, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return mcpsdk.NewToolResultError(fmt.Sprintf("bad 'from' (need RFC3339): %v", err)), nil
		}
		to, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return mcpsdk.NewToolResultError(fmt.Sprintf("bad 'to' (need RFC3339): %v", err)), nil
		}
		status, _ := req.RequireString("status") // optional
		includeDiscarded := false
		if v, err := req.RequireBool("include_discarded"); err == nil {
			includeDiscarded = v
		}
		limit := 0
		if v, err := req.RequireFloat("limit"); err == nil && v > 0 {
			limit = int(v)
		}

		notes, err := store.GetNotesInRange(ctx, from, to, status, limit, includeDiscarded)
		if err != nil {
			logger.Error("get_notes_in_range", "err", err)
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		out := make([]mcpNote, len(notes))
		for i, n := range notes {
			out[i] = toMCP(n)
		}
		return jsonResult(out)
	})
}

func registerSearch(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("search_notes",
		mcpsdk.WithDescription("Full-text search over note transcriptions using SQLite FTS5. "+
			"Query supports the FTS5 syntax: bare words (AND), \"phrase\", term*, term1 OR term2. "+
			"Results sorted by bm25 rank (lower is better). Each hit includes a 'snippet' "+
			"field — ~30 tokens around the match with the matched term wrapped in << >> "+
			"and elided context shown as '...'. Use 'snippet' for dense context; 'raw_text' "+
			"still carries the full note. Discarded notes are excluded by default — the "+
			"user explicitly marked them as 'forget this'. Set include_discarded=true only "+
			"if the user is asking about what they discarded."),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithString("query", mcpsdk.Required(),
			mcpsdk.Description("FTS5 MATCH expression."),
		),
		mcpsdk.WithNumber("limit",
			mcpsdk.Description("Maximum hits to return. Default 20."),
		),
		mcpsdk.WithBoolean("include_discarded",
			mcpsdk.Description("If true, include notes the user marked as discarded. Default false."),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		query, err := req.RequireString("query")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		limit := 20
		if v, err := req.RequireFloat("limit"); err == nil && v > 0 {
			limit = int(v)
		}
		includeDiscarded := false
		if v, err := req.RequireBool("include_discarded"); err == nil {
			includeDiscarded = v
		}
		hits, err := store.SearchNotes(ctx, query, limit, includeDiscarded)
		if err != nil {
			logger.Error("search_notes", "query", query, "err", err)
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		out := make([]mcpNote, len(hits))
		for i, h := range hits {
			m := toMCP(h.Note)
			m.Rank = h.Rank
			m.Snippet = h.Snippet
			out[i] = m
		}
		return jsonResult(out)
	})
}

func registerMarkAnalyzed(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("mark_analyzed",
		mcpsdk.WithDescription("Set status='analyzed' for the given note ids. "+
			"Discarded notes are not changed. Returns {updated: N}."),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithArray("ids", mcpsdk.Required(),
			mcpsdk.Description("List of note ids."),
			mcpsdk.Items(map[string]any{"type": "integer"}),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		raw := req.GetArguments()["ids"]
		ids, err := toInt64Slice(raw)
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		n, err := store.MarkAnalyzed(ctx, ids)
		if err != nil {
			logger.Error("mark_analyzed", "err", err)
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]int{"updated": n})
	})
}

func registerGetNote(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("get_note",
		mcpsdk.WithDescription("Fetch a single note by its ID. Returns the full note object "+
			"(including raw_text and current status). Use when you need full context for one "+
			"hit from search_notes."),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithNumber("id", mcpsdk.Required(),
			mcpsdk.Description("Note ID."),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		idF, err := req.RequireFloat("id")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		n, err := store.GetNote(ctx, int64(idF))
		if err != nil {
			if errors.Is(err, db.ErrNoteNotFound) {
				return mcpsdk.NewToolResultError(fmt.Sprintf("note %d not found", int64(idF))), nil
			}
			logger.Error("get_note", "err", err)
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		return jsonResult(toMCP(n))
	})
}

func registerDiscardNotes(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("discard_notes",
		mcpsdk.WithDescription("Mark the given note ids as discarded. Mirrors the bot's "+
			"/delete command but accepts a batch. Already-discarded rows are ignored. "+
			"Returns {updated: N} — the count of rows actually flipped. Reversible via restore_note."),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithArray("ids", mcpsdk.Required(),
			mcpsdk.Description("List of note ids."),
			mcpsdk.Items(map[string]any{"type": "integer"}),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		raw := req.GetArguments()["ids"]
		ids, err := toInt64Slice(raw)
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		n, err := store.DiscardNotes(ctx, ids)
		if err != nil {
			logger.Error("discard_notes", "err", err)
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]int{"updated": n})
	})
}

func registerRestoreNote(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("restore_note",
		mcpsdk.WithDescription("Restore a discarded note back to pending. Only acts on "+
			"notes whose current status is 'discarded'. Returns {restored: bool} — true if "+
			"the note was discarded and got flipped, false if it exists but was not "+
			"discarded. Errors if the id is unknown."),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithNumber("id", mcpsdk.Required(),
			mcpsdk.Description("Note ID to restore."),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		idF, err := req.RequireFloat("id")
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		id := int64(idF)
		ok, err := store.RestoreNote(ctx, id)
		if err != nil {
			if errors.Is(err, db.ErrNoteNotFound) {
				return mcpsdk.NewToolResultError(fmt.Sprintf("note %d not found", id)), nil
			}
			logger.Error("restore_note", "err", err)
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]bool{"restored": ok})
	})
}

// retranscribeResponse is what Claude gets back from a successful
// retranscribe call. old_text / new_text let it summarize the diff;
// confidence_overall surfaces whether the new run was actually better.
type retranscribeResponse struct {
	NoteID               int64    `json:"note_id"`
	OldText              string   `json:"old_text"`
	NewText              string   `json:"new_text"`
	ConfidenceOverall    *float64 `json:"confidence_overall,omitempty"`
	ConfidenceMin        *float64 `json:"confidence_min,omitempty"`
	SuspectHallucination bool     `json:"suspect_hallucination,omitempty"`
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
		prompt := composePrompt(ctx, store, deps.BasePrompt)
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

// composePrompt mirrors the bot's processFile prompt construction: a
// shared free-form admin prompt (basePrompt) plus the user-managed
// vocabulary, joined by a space. Empty vocabulary just returns basePrompt.
func composePrompt(ctx context.Context, store *db.DB, basePrompt string) string {
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	vocab, _ := store.VocabPrompt(dbCtx) // tolerate failures — base prompt alone is fine
	base := strings.TrimSpace(basePrompt)
	switch {
	case base == "" && vocab == "":
		return ""
	case base == "":
		return vocab
	case vocab == "":
		return base
	default:
		return base + " " + vocab
	}
}

func toInt64Slice(v any) ([]int64, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("ids must be an array")
	}
	out := make([]int64, 0, len(arr))
	for i, x := range arr {
		switch n := x.(type) {
		case float64:
			out = append(out, int64(n))
		case int:
			out = append(out, int64(n))
		case int64:
			out = append(out, n)
		case json.Number:
			parsed, err := n.Int64()
			if err != nil {
				return nil, fmt.Errorf("ids[%d]: %w", i, err)
			}
			out = append(out, parsed)
		default:
			return nil, fmt.Errorf("ids[%d]: want number, got %T", i, x)
		}
	}
	return out, nil
}

func jsonResult(v any) (*mcpsdk.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return mcpsdk.NewToolResultError(err.Error()), nil
	}
	return mcpsdk.NewToolResultText(string(b)), nil
}
