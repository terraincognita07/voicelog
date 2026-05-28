package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"voicelog/internal/db"
)

// Read-only MCP tools — no DB writes, no external calls, safe to call
// from any agent loop without side effects on the corpus.

func registerDBHealth(s *server.MCPServer, store *db.DB, logger *slog.Logger) {
	tool := mcpsdk.NewTool("db_health",
		mcpsdk.WithDescription("Run SQLite's integrity_check + quick_check, then report note count and "+
			"on-disk size. integrity_check / quick_check return the literal string \"ok\" on a healthy "+
			"DB; anything else is a real corruption signal. Cheap on a small DB — safe to call ad-hoc "+
			"(say, weekly) to verify the backup is still readable. Pass quick=true on a multi-GB DB "+
			"to skip the slow full integrity_check; quick_check still runs."),
		mcpsdk.WithReadOnlyHintAnnotation(true),
		mcpsdk.WithDestructiveHintAnnotation(false),
		mcpsdk.WithIdempotentHintAnnotation(true),
		mcpsdk.WithOpenWorldHintAnnotation(false),
		mcpsdk.WithBoolean("quick",
			mcpsdk.Description("If true, skip the full integrity_check (which can take >30s on a multi-GB DB) and only run quick_check + count + size. Default false."),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		// Pick the right deadline based on what the caller asked for.
		// integrity_check (the default, slow path) gets 30s; quick mode
		// only needs ~2s for quick_check + counts.
		quick := false
		if v, err := req.RequireBool("quick"); err == nil {
			quick = v
		}
		timeout := 30 * time.Second
		if quick {
			timeout = 2 * time.Second
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		rep, err := store.Health(ctx, quick)
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
