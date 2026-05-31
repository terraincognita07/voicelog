package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/terraincognita07/voicelog/internal/db"
)

// TestAddTags_NormalizeAndSkip exercises tag normalization through the public
// API: case-fold, strip a leading '#', dedup, and skip empty / '#'-only /
// over-length tags. (normalizeTag itself is unexported; this is its contract.)
func TestAddTags_NormalizeAndSkip(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id, _ := d.InsertNote(ctx, "x", 1)

	added, err := d.AddTags(ctx, id, []string{
		"#TODO", "todo", "", "#", "  ", strings.Repeat("a", db.MaxTagLen+1),
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added != 1 {
		t.Fatalf("want 1 added (todo only; rest dup/empty/over-length), got %d", added)
	}
	tags, _ := d.TagsForNote(ctx, id)
	if len(tags) != 1 || tags[0] != "todo" {
		t.Fatalf("tags = %v, want [todo]", tags)
	}
}

func TestAddTagsAndTagsForNote(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id, _ := d.InsertNote(ctx, "a philosophical musing", 3)

	added, err := d.AddTags(ctx, id, []string{"#Философия", "идея", "философия"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if added != 2 {
		t.Fatalf("want 2 added (философия + идея; the 3rd dups философия), got %d", added)
	}
	tags, _ := d.TagsForNote(ctx, id)
	if len(tags) != 2 || tags[0] != "идея" || tags[1] != "философия" {
		t.Fatalf("tags = %v, want [идея философия] (alphabetical)", tags)
	}

	// Re-adding an existing tag is idempotent.
	if added, _ := d.AddTags(ctx, id, []string{"идея"}); added != 0 {
		t.Errorf("re-add should add 0, got %d", added)
	}

	// Unknown note id → ErrNoteNotFound.
	if _, err := d.AddTags(ctx, 99999, []string{"x"}); !errors.Is(err, db.ErrNoteNotFound) {
		t.Errorf("want ErrNoteNotFound for unknown id, got %v", err)
	}
}

func TestRemoveTag(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id, _ := d.InsertNote(ctx, "x", 1)
	_, _ = d.AddTags(ctx, id, []string{"todo", "важное"})

	ok, err := d.RemoveTag(ctx, id, "#TODO") // normalize matches stored "todo"
	if err != nil || !ok {
		t.Fatalf("remove: ok=%v err=%v", ok, err)
	}
	tags, _ := d.TagsForNote(ctx, id)
	if len(tags) != 1 || tags[0] != "важное" {
		t.Fatalf("after remove: %v, want [важное]", tags)
	}
	if ok, _ := d.RemoveTag(ctx, id, "nope"); ok {
		t.Errorf("removing an absent tag should return false")
	}
}

func TestTagsForNotesBatch(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id1, _ := d.InsertNote(ctx, "one", 1)
	id2, _ := d.InsertNote(ctx, "two", 1)
	id3, _ := d.InsertNote(ctx, "three (untagged)", 1)
	_, _ = d.AddTags(ctx, id1, []string{"a", "b"})
	_, _ = d.AddTags(ctx, id2, []string{"c"})

	m, err := d.TagsForNotes(ctx, []int64{id1, id2, id3})
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if len(m[id1]) != 2 || len(m[id2]) != 1 {
		t.Fatalf("batch map wrong: %v", m)
	}
	if _, ok := m[id3]; ok {
		t.Errorf("untagged note must be absent from the map, got %v", m[id3])
	}
	if mm, err := d.TagsForNotes(ctx, nil); err != nil || len(mm) != 0 {
		t.Errorf("empty ids: m=%v err=%v", mm, err)
	}
}

func TestListTags(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id1, _ := d.InsertNote(ctx, "1", 1)
	id2, _ := d.InsertNote(ctx, "2", 1)
	_, _ = d.AddTags(ctx, id1, []string{"идея", "todo"})
	_, _ = d.AddTags(ctx, id2, []string{"идея"})

	got, err := d.ListTags(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 distinct tags, got %d: %v", len(got), got)
	}
	// Most-used first: идея (2) before todo (1).
	if got[0].Tag != "идея" || got[0].Count != 2 {
		t.Errorf("first tag should be идея/2, got %+v", got[0])
	}
	if got[1].Tag != "todo" || got[1].Count != 1 {
		t.Errorf("second tag should be todo/1, got %+v", got[1])
	}
}

func TestNotesByTag(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id1, _ := d.InsertNote(ctx, "older philosophical note", 1)
	id2, _ := d.InsertNote(ctx, "newer philosophical note", 1)
	_, _ = d.InsertNote(ctx, "unrelated", 1)
	_, _ = d.AddTags(ctx, id1, []string{"философия"})
	_, _ = d.AddTags(ctx, id2, []string{"философия"})

	// Normalize: "#Философия" matches stored "философия".
	notes, err := d.NotesByTag(ctx, "#Философия", 0)
	if err != nil {
		t.Fatalf("by tag: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("want 2 tagged notes, got %d", len(notes))
	}
	// Newest first (id DESC tiebreak when created_at collides at 1s resolution).
	if notes[0].ID != id2 || notes[1].ID != id1 {
		t.Errorf("order should be newest-first: got %d, %d", notes[0].ID, notes[1].ID)
	}
	if _, err := d.NotesByTag(ctx, "  ", 10); err == nil {
		t.Errorf("empty tag should error")
	}
}

func TestDeleteNoteCascadesTags(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id, _ := d.InsertNote(ctx, "to delete", 1)
	_, _ = d.AddTags(ctx, id, []string{"a", "b"})

	if _, err := d.DeleteNote(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// ON DELETE CASCADE must drop the note's tags.
	if m, _ := d.TagsForNotes(ctx, []int64{id}); len(m) != 0 {
		t.Errorf("tags should be gone after note delete (CASCADE), got %v", m)
	}
}
