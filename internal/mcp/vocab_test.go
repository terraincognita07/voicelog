package mcp_test

import (
	"strings"
	"testing"
)

func TestAddAndListVocab(t *testing.T) {
	f := newFixture(t)

	tcr := callTool(t, f, "add_vocab", map[string]any{
		"terms": []any{"Иннокентий", "voicelog"},
	})
	var added map[string]int
	decodePayload(t, tcr, &added)
	if added["added"] != 2 {
		t.Fatalf("want 2 added, got %+v", added)
	}

	tcr = callTool(t, f, "list_vocab", map[string]any{})
	var listed struct {
		Terms []string `json:"terms"`
		Count int      `json:"count"`
	}
	decodePayload(t, tcr, &listed)
	if listed.Count != 2 {
		t.Fatalf("want count 2, got %d (%v)", listed.Count, listed.Terms)
	}
	joined := strings.Join(listed.Terms, " ")
	if !strings.Contains(joined, "Иннокентий") || !strings.Contains(joined, "voicelog") {
		t.Fatalf("listed terms missing entries: %v", listed.Terms)
	}
}

func TestAddVocab_DedupAndTooLong(t *testing.T) {
	f := newFixture(t)

	callTool(t, f, "add_vocab", map[string]any{"terms": []any{"проект"}})
	tcr := callTool(t, f, "add_vocab", map[string]any{
		"terms": []any{"проект", strings.Repeat("x", 65)},
	})
	var r map[string]int
	decodePayload(t, tcr, &r)
	if r["added"] != 0 {
		t.Errorf("re-adding existing term should add 0, got %d", r["added"])
	}
	if r["skipped_existing"] != 1 {
		t.Errorf("want 1 skipped_existing, got %d", r["skipped_existing"])
	}
	if r["skipped_too_long"] != 1 {
		t.Errorf("want 1 skipped_too_long, got %d", r["skipped_too_long"])
	}
}

func TestRemoveVocab(t *testing.T) {
	f := newFixture(t)
	callTool(t, f, "add_vocab", map[string]any{"terms": []any{"Коля"}})

	tcr := callTool(t, f, "remove_vocab", map[string]any{"term": "коля"}) // case-insensitive
	var r map[string]bool
	decodePayload(t, tcr, &r)
	if !r["removed"] {
		t.Fatal("expected removed=true for an existing term (case-insensitive)")
	}

	tcr = callTool(t, f, "remove_vocab", map[string]any{"term": "Коля"})
	decodePayload(t, tcr, &r)
	if r["removed"] {
		t.Fatal("removing an absent term should report removed=false")
	}
}

func TestAddVocab_MissingTermsArg(t *testing.T) {
	f := newFixture(t)
	tcr := callTool(t, f, "add_vocab", map[string]any{})
	msg := expectToolError(t, tcr)
	if !strings.Contains(msg, "array") {
		t.Errorf("expected an 'array' error, got %q", msg)
	}
}
