package mcp_test

import (
	"strings"
	"testing"
	"time"
)

func TestTagNote_AddsAndListTags(t *testing.T) {
	f := newFixture(t)
	id := seedNote(t, f.store, time.Now(), "a thought")

	tcr := callTool(t, f, "tag_note", map[string]any{
		"id":   float64(id),
		"tags": []any{"#Философия", "идея", "философия"},
	})
	var got map[string]int
	decodePayload(t, tcr, &got)
	if got["added"] != 2 {
		t.Errorf("added: want 2 (dup философия ignored), got %d", got["added"])
	}

	tcr = callTool(t, f, "list_tags", map[string]any{})
	var tags []map[string]any
	decodePayload(t, tcr, &tags)
	if len(tags) != 2 {
		t.Errorf("list_tags: want 2 distinct tags, got %d", len(tags))
	}
}

func TestTagNote_UnknownIDErrors(t *testing.T) {
	f := newFixture(t)
	tcr := callTool(t, f, "tag_note", map[string]any{
		"id":   float64(99999),
		"tags": []any{"x"},
	})
	msg := expectToolError(t, tcr)
	if !strings.Contains(msg, "not found") {
		t.Errorf("want not-found error, got %q", msg)
	}
}

func TestUntagNote(t *testing.T) {
	f := newFixture(t)
	id := seedNote(t, f.store, time.Now(), "x")
	callTool(t, f, "tag_note", map[string]any{"id": float64(id), "tags": []any{"todo"}})

	tcr := callTool(t, f, "untag_note", map[string]any{"id": float64(id), "tag": "#TODO"})
	var got map[string]bool
	decodePayload(t, tcr, &got)
	if !got["removed"] {
		t.Errorf("removed: want true")
	}
	// Removing again → false.
	tcr = callTool(t, f, "untag_note", map[string]any{"id": float64(id), "tag": "todo"})
	decodePayload(t, tcr, &got)
	if got["removed"] {
		t.Errorf("second remove: want false")
	}
}

func TestNotesByTag_DeterministicAndCarriesTags(t *testing.T) {
	f := newFixture(t)
	id := seedNote(t, f.store, time.Now(), "time is a construct")
	seedNote(t, f.store, time.Now(), "unrelated note")
	callTool(t, f, "tag_note", map[string]any{"id": float64(id), "tags": []any{"философия"}})

	tcr := callTool(t, f, "notes_by_tag", map[string]any{"tag": "#Философия"})
	var got []map[string]any
	decodePayload(t, tcr, &got)
	if len(got) != 1 {
		t.Fatalf("want 1 note by tag, got %d", len(got))
	}
	if int64(got[0]["id"].(float64)) != id {
		t.Errorf("wrong note returned: %v", got[0]["id"])
	}
	tags, ok := got[0]["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0].(string) != "философия" {
		t.Errorf("note should carry its tags, got %v", got[0]["tags"])
	}
}

func TestGetNote_IncludesTags(t *testing.T) {
	f := newFixture(t)
	id := seedNote(t, f.store, time.Now(), "tagged note")
	callTool(t, f, "tag_note", map[string]any{"id": float64(id), "tags": []any{"идея"}})

	tcr := callTool(t, f, "get_note", map[string]any{"id": float64(id)})
	var got map[string]any
	decodePayload(t, tcr, &got)
	tags, ok := got["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0].(string) != "идея" {
		t.Errorf("get_note should include tags, got %v", got["tags"])
	}
}
