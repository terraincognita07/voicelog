package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/terraincognita07/voicelog/internal/db"
)

// Mutating MCP tools — they write to the DB but do not call out to
// external services. Each one is idempotent at the row level
// (re-marking an already-discarded note is a no-op) so retries from
// the agent loop are safe.

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
