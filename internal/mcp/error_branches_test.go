package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/terraincognita07/voicelog/internal/mcp"
	"github.com/terraincognita07/voicelog/internal/whisper"
)

// Error-branch coverage for the MCP tools.
//
// The happy-path tests in server_test.go cover the "everything works"
// shape of every tool. These tests target the second half of every
// handler: argument validation failures, missing rows, the no-audio
// guard inside retranscribe, and the DB-closed propagation that the
// logger.Error + NewToolResultError chain hides on a healthy instance.

// --- toInt64Slice (server.go) -------------------------------------------

func TestMarkAnalyzed_IdsNotArrayIsRejected(t *testing.T) {
	// `ids` is supposed to be a JSON array; sending a scalar must surface
	// "ids must be an array" rather than crashing the handler.
	f := newFixture(t)
	tcr := callTool(t, f, "mark_analyzed", map[string]any{"ids": 42})
	msg := expectToolError(t, tcr)
	if !strings.Contains(msg, "array") {
		t.Errorf("error should mention 'array': %q", msg)
	}
}

func TestMarkAnalyzed_NonNumericIDIsRejected(t *testing.T) {
	f := newFixture(t)
	tcr := callTool(t, f, "mark_analyzed", map[string]any{
		"ids": []any{1, "two", 3},
	})
	msg := expectToolError(t, tcr)
	if !strings.Contains(msg, "ids[1]") {
		t.Errorf("error should pinpoint the bad element at ids[1]: %q", msg)
	}
}

func TestDeleteNotes_EmptyIdsReturnsZero(t *testing.T) {
	// Empty array is valid input; nothing is deleted. deleted must come
	// back as 0, not be reported as an error.
	f := newFixture(t)
	tcr := callTool(t, f, "delete_notes", map[string]any{"ids": []any{}})
	var got map[string]int
	decodePayload(t, tcr, &got)
	if got["deleted"] != 0 {
		t.Errorf("deleted: want 0, got %d", got["deleted"])
	}
}

// --- get_notes_in_range error / alternate branches ----------------------

func TestGetNotesInRange_BadFromIsRejected(t *testing.T) {
	f := newFixture(t)
	tcr := callTool(t, f, "get_notes_in_range", map[string]any{
		"from": "not-a-date",
		"to":   time.Now().UTC().Format(time.RFC3339),
	})
	msg := expectToolError(t, tcr)
	if !strings.Contains(msg, "'from'") {
		t.Errorf("error should pinpoint 'from': %q", msg)
	}
}

func TestGetNotesInRange_BadToIsRejected(t *testing.T) {
	f := newFixture(t)
	tcr := callTool(t, f, "get_notes_in_range", map[string]any{
		"from": time.Now().UTC().Format(time.RFC3339),
		"to":   "also-not-a-date",
	})
	msg := expectToolError(t, tcr)
	if !strings.Contains(msg, "'to'") {
		t.Errorf("error should pinpoint 'to': %q", msg)
	}
}

func TestGetNotesInRange_StatusFilterExplicit(t *testing.T) {
	// An explicit status filter narrows to just that status. Seed one
	// pending and one analyzed, filter status="analyzed", expect only it.
	f := newFixture(t)
	now := time.Now()
	seedNote(t, f.store, now, "pending one")
	analyzedID := seedNote(t, f.store, now, "to be analyzed")
	if _, err := f.store.MarkAnalyzed(context.Background(), []int64{analyzedID}); err != nil {
		t.Fatalf("mark analyzed: %v", err)
	}
	from := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	to := now.Add(1 * time.Hour).UTC().Format(time.RFC3339)
	tcr := callTool(t, f, "get_notes_in_range", map[string]any{
		"from":   from,
		"to":     to,
		"status": "analyzed",
	})
	var got []map[string]any
	decodePayload(t, tcr, &got)
	if len(got) != 1 {
		t.Fatalf("want 1 analyzed, got %d", len(got))
	}
	if int64(got[0]["id"].(float64)) != analyzedID {
		t.Errorf("id: want %d, got %v", analyzedID, got[0]["id"])
	}
}

func TestGetNotesInRange_LimitClampsResults(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		seedNote(t, f.store, now.Add(-time.Duration(i)*time.Minute), "n")
	}
	from := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	to := now.Add(1 * time.Hour).UTC().Format(time.RFC3339)
	tcr := callTool(t, f, "get_notes_in_range", map[string]any{
		"from":  from,
		"to":    to,
		"limit": float64(2),
	})
	var got []map[string]any
	decodePayload(t, tcr, &got)
	if len(got) != 2 {
		t.Errorf("limit=2 must cap result count; got %d", len(got))
	}
}

// --- search_notes alternate paths ---------------------------------------

func TestSearchNotes_EmptyHitsReturnsEmptyArray(t *testing.T) {
	// A perfectly valid query with zero matches must return [] rather
	// than an error — Claude relies on the empty-array signal.
	f := newFixture(t)
	tcr := callTool(t, f, "search_notes", map[string]any{"query": "noresultsterm"})
	var got []map[string]any
	decodePayload(t, tcr, &got)
	if len(got) != 0 {
		t.Errorf("want 0 hits, got %d", len(got))
	}
}

// --- retranscribe error branches ----------------------------------------

func TestRetranscribe_NoteNotFound(t *testing.T) {
	// With a non-nil whisper the handler advances past the "unavailable"
	// short-circuit and into GetNote. An unknown id surfaces as
	// "note N not found".
	deps := mcp.RetranscribeDeps{Whisper: &whisper.Client{}}
	f := newFixtureWith(t, deps)
	tcr := callTool(t, f, "retranscribe", map[string]any{"id": float64(99999)})
	msg := expectToolError(t, tcr)
	if !strings.Contains(msg, "not found") {
		t.Errorf("error should say not found: %q", msg)
	}
}

func TestRetranscribe_NoRetainedAudio(t *testing.T) {
	// Seed a pending note without setting audio_path. retranscribe must
	// refuse with the "no retained audio" hint pointing the operator at
	// AUDIO_RETENTION_DAYS.
	deps := mcp.RetranscribeDeps{Whisper: &whisper.Client{}}
	f := newFixtureWith(t, deps)
	id := seedNote(t, f.store, time.Now(), "text without audio_path")
	tcr := callTool(t, f, "retranscribe", map[string]any{"id": float64(id)})
	msg := expectToolError(t, tcr)
	if !strings.Contains(msg, "no retained audio") {
		t.Errorf("error should mention 'no retained audio': %q", msg)
	}
	if !strings.Contains(msg, "AUDIO_RETENTION_DAYS") {
		t.Errorf("error should hint at AUDIO_RETENTION_DAYS: %q", msg)
	}
}

func TestRetranscribe_MissingIDIsRejected(t *testing.T) {
	deps := mcp.RetranscribeDeps{Whisper: &whisper.Client{}}
	f := newFixtureWith(t, deps)
	tcr := callTool(t, f, "retranscribe", map[string]any{})
	msg := expectToolError(t, tcr)
	if msg == "" {
		t.Errorf("missing id must surface a validation message")
	}
}

// --- get_note error branches --------------------------------------------

func TestGetNote_MissingIDIsRejected(t *testing.T) {
	f := newFixture(t)
	tcr := callTool(t, f, "get_note", map[string]any{})
	msg := expectToolError(t, tcr)
	if msg == "" {
		t.Errorf("missing id must surface a validation message")
	}
}

// --- toMCP confidence-fields path ---------------------------------------

func TestGetNote_ConfidenceFieldsRoundtrip(t *testing.T) {
	// Seed a note via direct SQL with non-null confidence columns so toMCP
	// hits the *pointer assignment branches that the all-NULL happy-path
	// notes never exercise.
	f := newFixture(t)
	now := time.Now()
	res, err := f.store.ExecContext(context.Background(),
		`INSERT INTO notes (created_at, raw_text, duration_sec,
		                    confidence_overall, confidence_min, suspect_hallucination)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		now.Unix(), "with confidence", 5, -0.12, -0.34, 1)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	id, _ := res.LastInsertId()

	tcr := callTool(t, f, "get_note", map[string]any{"id": float64(id)})
	var got map[string]any
	decodePayload(t, tcr, &got)
	if got["confidence_overall"] == nil {
		t.Errorf("confidence_overall must be present when not NULL; got %+v", got)
	}
	if got["confidence_min"] == nil {
		t.Errorf("confidence_min must be present when not NULL; got %+v", got)
	}
	if got["suspect_hallucination"] != true {
		t.Errorf("suspect_hallucination: want true, got %v", got["suspect_hallucination"])
	}
}

// --- DB-closed propagation ---------------------------------------------

func TestListPendingNotes_PropagatesDBClosed(t *testing.T) {
	// Closing the *db.DB under a live MCP server forces every tool to
	// surface the underlying sql error rather than hang or panic. This
	// is the path operators see if the DB file is moved/locked out from
	// under a running server — important to assert clean behavior.
	f := newFixture(t)
	_ = f.store.Close()
	tcr := callTool(t, f, "list_pending_notes", map[string]any{"limit": 5})
	msg := expectToolError(t, tcr)
	// Sanitized (#2): the raw "sql: database is closed" must not reach the
	// caller — only the generic "<op> failed — see server logs" form does.
	if !strings.Contains(msg, "see server logs") {
		t.Errorf("closed-DB error must be sanitized, got %q", msg)
	}
}

func TestSearchNotes_PropagatesDBClosed(t *testing.T) {
	f := newFixture(t)
	_ = f.store.Close()
	tcr := callTool(t, f, "search_notes", map[string]any{"query": "anything"})
	msg := expectToolError(t, tcr)
	// search_notes is the documented exception to #2: its errors (FTS5 query
	// syntax, or a closed DB here) surface verbatim so the caller can fix the
	// query. Assert only that a non-empty error came back.
	if msg == "" {
		t.Errorf("expected non-empty error from a closed DB")
	}
}

func TestMarkAnalyzed_PropagatesDBClosed(t *testing.T) {
	f := newFixture(t)
	_ = f.store.Close()
	tcr := callTool(t, f, "mark_analyzed", map[string]any{"ids": []any{1}})
	msg := expectToolError(t, tcr)
	// Sanitized (#2): the raw "sql: database is closed" must not reach the
	// caller — only the generic "<op> failed — see server logs" form does.
	if !strings.Contains(msg, "see server logs") {
		t.Errorf("closed-DB error must be sanitized, got %q", msg)
	}
}

func TestDBHealth_PropagatesDBClosed(t *testing.T) {
	f := newFixture(t)
	_ = f.store.Close()
	tcr := callTool(t, f, "db_health", nil)
	msg := expectToolError(t, tcr)
	// Sanitized (#2): the raw "sql: database is closed" must not reach the
	// caller — only the generic "<op> failed — see server logs" form does.
	if !strings.Contains(msg, "see server logs") {
		t.Errorf("closed-DB error must be sanitized, got %q", msg)
	}
}

// TestSearchNotes_FTS5ErrorSurfacesVerbatim proves the documented exception
// to #2: a malformed FTS5 MATCH is the caller's own query problem (no path /
// schema leak), so search_notes returns it verbatim — NOT the generic
// "see server logs" — so the caller can fix the syntax. A trailing boolean
// operator is a reliable FTS5 syntax error.
func TestSearchNotes_FTS5ErrorSurfacesVerbatim(t *testing.T) {
	f := newFixture(t)
	seedNote(t, f.store, time.Now(), "anything to index")
	tcr := callTool(t, f, "search_notes", map[string]any{"query": "foo AND"})
	msg := expectToolError(t, tcr)
	if strings.Contains(msg, "see server logs") {
		t.Errorf("FTS5 query error must reach the caller verbatim, got sanitized: %q", msg)
	}
	if msg == "" {
		t.Errorf("expected a non-empty FTS5 error message")
	}
}
