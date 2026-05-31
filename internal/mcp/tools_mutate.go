package mcp

import (
	"context"
	"log/slog"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/terraincognita07/voicelog/internal/audio"
	"github.com/terraincognita07/voicelog/internal/db"
)

// Mutating MCP tools — they write to the DB but do not call out to
// external services. mark_analyzed is idempotent at the row level (a
// re-mark of an already-analyzed note is a no-op); delete_notes is a
// permanent, irreversible removal.

func registerMarkAnalyzed(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("mark_analyzed",
		mcpsdk.WithDescription("Set status='analyzed' for the given note ids. "+
			"Only pending notes flip; already-analyzed ids are ignored. Returns {updated: N}."),
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
			return opFailed("mark_analyzed"), nil
		}
		return jsonResult(map[string]int{"updated": n})
	})
}

func registerDeleteNotes(s *server.MCPServer, store *db.DB, audioDir string, logger *slog.Logger) {
	tool := mcpsdk.NewTool("delete_notes",
		mcpsdk.WithDescription("PERMANENTLY delete the given note ids — the transcription, its edit "+
			"history, and any retained audio file are all removed. This is IRREVERSIBLE: there is no "+
			"restore. Only delete notes the user has clearly finished with or explicitly asked to "+
			"erase. Unknown ids are ignored. Returns {deleted: N} — the count of rows actually removed."),
		mcpsdk.WithReadOnlyHintAnnotation(false),
		mcpsdk.WithDestructiveHintAnnotation(true),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithArray("ids", mcpsdk.Required(),
			mcpsdk.Description("List of note ids to delete permanently."),
			mcpsdk.Items(map[string]any{"type": "integer"}),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		raw := req.GetArguments()["ids"]
		ids, err := toInt64Slice(raw)
		if err != nil {
			return mcpsdk.NewToolResultError(err.Error()), nil
		}
		paths, n, err := store.DeleteNotes(ctx, ids)
		if err != nil {
			logger.Error("delete_notes", "err", err)
			return opFailed("delete_notes"), nil
		}
		// Best-effort on-disk cleanup; never fails the tool over a stuck file.
		for _, p := range paths {
			audio.Delete(audioDir, p, logger)
		}
		return jsonResult(map[string]int{"deleted": n})
	})
}
