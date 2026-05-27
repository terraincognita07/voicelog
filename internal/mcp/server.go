package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"voicelog/internal/db"
)

const (
	serverName    = "voicelog"
	serverVersion = "0.1.0"
)

type mcpNote struct {
	ID           int64   `json:"id"`
	CreatedAtISO string  `json:"created_at_iso"`
	RawText      string  `json:"raw_text"`
	DurationSec  int64   `json:"duration_sec"`
	Status       string  `json:"status,omitempty"`
	Rank         float64 `json:"rank,omitempty"`
	Snippet      string  `json:"snippet,omitempty"`
}

func toMCP(n db.Note) mcpNote {
	return mcpNote{
		ID:           n.ID,
		CreatedAtISO: n.CreatedAt.UTC().Format(time.RFC3339),
		RawText:      n.RawText,
		DurationSec:  n.DurationSec.Int64,
		Status:       string(n.Status),
	}
}

// NewServer builds the MCP server and registers all four voicelog tools.
func NewServer(store *db.DB, logger *slog.Logger) *server.MCPServer {
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

	return s
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
			"Optional status filter: pending | analyzed | discarded."),
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
			mcpsdk.Description("Optional status filter."),
			mcpsdk.Enum("pending", "analyzed", "discarded"),
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
		limit := 0
		if v, err := req.RequireFloat("limit"); err == nil && v > 0 {
			limit = int(v)
		}

		notes, err := store.GetNotesInRange(ctx, from, to, status, limit)
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
			"still carries the full note."),
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
		hits, err := store.SearchNotes(ctx, query, limit)
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
