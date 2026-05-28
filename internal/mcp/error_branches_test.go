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

func TestDiscardNotes_EmptyIdsReturnsZero(t *testing.T) {
	// Empty array is valid input; the underlying UPDATE matches 0 rows.
	// updated must come back as 0, not be reported as an error.
	f := newFixture(t)
	tcr := callTool(t, f, "discard_notes", map[string]any{"ids": []any{}})
	var got map[string]int
	decodePayload(t, tcr, &got)
	if got["updated"] != 0 {
		t.Errorf("updated: want 0, got %d", got["updated"])
	}
}

// --- restore_note error branches ----------------------------------------

func TestRestoreNote_MissingIDIsRejected(t *testing.T) {
	// id is Required — omitting it must surface a validation message.
	f := newFixture(t)
	tcr := callTool(t, f, "restore_note", map[string]any{})
	msg := expectToolError(t, tcr)
	if msg == "" {
		t.Errorf("error message must be non-empty")
	}
}

func TestRestoreNote_UnknownIDIsErrNoteNotFound(t *testing.T) {
	f := newFixture(t)
	tcr := callTool(t, f, "restore_note", map[string]any{"id": float64(99999)})
	msg := expectToolError(t, tcr)
	if !strings.Contains(msg, "not found") {
		t.Errorf("error should say not found: %q", msg)
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
	// status="discarded" overrides the default exclusion. Seed one of
	// each kind and confirm only the discarded note comes back.
	f := newFixture(t)
	now := time.Now()
	seedNote(t, f.store, now, "pending one")
	discardID := seedNote(t, f.store, now, "to be discarded")
	if _, err := f.store.DiscardNotes(context.Background(), []int64{discardID}); err != nil {
		t.Fatalf("discard: %v", err)
	}
	from := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	to := now.Add(1 * time.Hour).UTC().Format(time.RFC3339)
	tcr := callTool(t, f, "get_notes_in_range", map[string]any{
		"from":   from,
		"to":     to,
		"status": "discarded",
	})
	var got []map[string]any
	decodePayload(t, tcr, &got)
	if len(got) != 1 {
		t.Fatalf("want 1 discarded, got %d", len(got))
	}
	if int64(got[0]["id"].(float64)) != discardID {
		t.Errorf("id: want %d, got %v", discardID, got[0]["id"])
	}
}

func TestGetNotesInRange_IncludeDiscardedTrueShowsAll(t *testing.T) {
	// include_discarded=true (without an explicit status filter) widens
	// the result set to include discarded rows alongside pending/analyzed.
	f := newFixture(t)
	now := time.Now()
	seedNote(t, f.store, now, "alive")
	discardID := seedNote(t, f.store, now, "buried")
	if _, err := f.store.DiscardNotes(context.Background(), []int64{discardID}); err != nil {
		t.Fatalf("discard: %v", err)
	}
	from := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	to := now.Add(1 * time.Hour).UTC().Format(time.RFC3339)
	tcr := callTool(t, f, "get_notes_in_range", map[string]any{
		"from":              from,
		"to":                to,
		"include_discarded": true,
	})
	var got []map[string]any
	decodePayload(t, tcr, &got)
	if len(got) != 2 {
		t.Errorf("include_discarded=true must surface both rows; got %d", len(got))
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

func TestSearchNotes_IncludeDiscardedTrue(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	id := seedNote(t, f.store, now, "молоко purchase")
	if _, err := f.store.DiscardNotes(context.Background(), []int64{id}); err != nil {
		t.Fatalf("discard: %v", err)
	}
	tcr := callTool(t, f, "search_notes", map[string]any{
		"query":             "молоко",
		"include_discarded": true,
	})
	var got []map[string]any
	decodePayload(t, tcr, &got)
	if len(got) != 1 {
		t.Errorf("include_discarded=true must show the hit; got %d", len(got))
	}
}

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
	// "note N not found" — same wording as restore_note's branch.
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
	if msg == "" {
		t.Errorf("expected non-empty error from a closed DB")
	}
}

func TestSearchNotes_PropagatesDBClosed(t *testing.T) {
	f := newFixture(t)
	_ = f.store.Close()
	tcr := callTool(t, f, "search_notes", map[string]any{"query": "anything"})
	msg := expectToolError(t, tcr)
	if msg == "" {
		t.Errorf("expected non-empty error from a closed DB")
	}
}

func TestMarkAnalyzed_PropagatesDBClosed(t *testing.T) {
	f := newFixture(t)
	_ = f.store.Close()
	tcr := callTool(t, f, "mark_analyzed", map[string]any{"ids": []any{1}})
	msg := expectToolError(t, tcr)
	if msg == "" {
		t.Errorf("expected non-empty error from a closed DB")
	}
}

func TestDBHealth_PropagatesDBClosed(t *testing.T) {
	f := newFixture(t)
	_ = f.store.Close()
	tcr := callTool(t, f, "db_health", nil)
	msg := expectToolError(t, tcr)
	if msg == "" {
		t.Errorf("expected non-empty error from a closed DB")
	}
}
