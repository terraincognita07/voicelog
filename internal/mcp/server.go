// Package mcp builds the voicelog MCP server and registers every tool
// it exposes. The file you're reading carries the constructor and the
// shared helpers; the individual tools live next to each other by
// family:
//
//	tools_read.go         — list_pending_notes / get_notes_in_range /
//	                        search_notes / get_note / db_health
//	tools_mutate.go       — mark_analyzed / delete_notes
//	tools_vocab.go        — list_vocab / add_vocab / remove_vocab
//	tools_tags.go         — tag_note / untag_note / list_tags / notes_by_tag
//	tools_retranscribe.go — retranscribe (+ RetranscribeDeps)
//	auth.go               — BearerAuth wrapper used by cmd/mcp
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/terraincognita07/voicelog/internal/db"
)

const (
	serverName    = "voicelog"
	serverVersion = "0.5.0"
)

// mcpNote is the wire shape every voicelog tool returns. Mirrors the
// fields of db.Note but with JSON tags chosen for Claude's prompt
// surface (snake_case, optional fields omitted instead of zero-valued).
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
	Tags                 []string `json:"tags,omitempty"` // category labels (analysis-side overlay)
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
//
// Tools are listed in a registrar table so adding a new one is a one-
// line change in the right column instead of touching the constructor
// body. retranscribe stays out of the table because it takes deps;
// every other tool only needs (store, logger).
func NewServer(store *db.DB, deps RetranscribeDeps, logger *slog.Logger) *server.MCPServer {
	s := server.NewMCPServer(serverName, serverVersion,
		server.WithToolCapabilities(true),
	)

	type registrar func(s *server.MCPServer, store *db.DB, logger *slog.Logger)
	for _, register := range []registrar{
		registerListPending,
		registerGetRange,
		registerSearch,
		registerGetNote,
		registerMarkAnalyzed,
		registerDBHealth,
		registerListVocab,
		registerAddVocab,
		registerRemoveVocab,
		registerTagNote,
		registerUntagNote,
		registerListTags,
		registerNotesByTag,
	} {
		register(s, store, logger)
	}
	// delete_notes and retranscribe take extra deps (audio dir / whisper),
	// so they live outside the (store, logger) registrar table.
	registerDeleteNotes(s, store, deps.AudioDir, logger)
	registerRetranscribe(s, store, deps, logger)

	return s
}

// toInt64Slice converts a JSON-decoded `ids` argument into a []int64.
// The JSON-RPC layer hands us `any` for variadic args, so a few number
// types may show up depending on encoder. Anything else → typed error
// the caller can surface verbatim.
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

// attachTags batch-loads tags for the given mcpNotes and fills their Tags
// field — one query for the whole page, no N+1. Best-effort: the caller logs
// and proceeds on error, since tags are an enrichment, not the payload.
func attachTags(ctx context.Context, store *db.DB, notes []mcpNote) error {
	if len(notes) == 0 {
		return nil
	}
	ids := make([]int64, len(notes))
	for i, n := range notes {
		ids[i] = n.ID
	}
	tagMap, err := store.TagsForNotes(ctx, ids)
	if err != nil {
		return err
	}
	for i := range notes {
		if t := tagMap[notes[i].ID]; len(t) > 0 {
			notes[i].Tags = t
		}
	}
	return nil
}

// jsonResult marshals v and wraps it in an MCP text-content result.
// Every successful tool reply funnels through here so the wire shape
// stays consistent (one text part per response, JSON inside).
func jsonResult(v any) (*mcpsdk.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return mcpsdk.NewToolResultError(err.Error()), nil
	}
	return mcpsdk.NewToolResultText(string(b)), nil
}
