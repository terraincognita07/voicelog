package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"
)

// TestCbOpenCard_RendersActions: tapping a note in a list opens the card with
// the full action set (Edit / Tags / Delete / Back) and shows text + tags.
func TestCbOpenCard_RendersActions(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "card me", 0)
	_, _ = tb.db.AddTags(ctx, id, []string{"идея"})

	ref := cardRef{id: id, kind: "p", state: pendingState{Limit: pendingPageSize}.encode()}
	fc := &fakeCtx{callback: &tele.Callback{Data: ref.encode()}}
	if err := tb.cbOpenCard(fc); err != nil {
		t.Fatalf("cbOpenCard: %v", err)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an Edit (the card)")
	}
	body := ed.What.(string)
	if !strings.Contains(body, "card me") || !strings.Contains(body, "идея") {
		t.Errorf("card body must show text + tags: %q", body)
	}
	mk, _ := ed.Opts[0].(*tele.ReplyMarkup)
	for _, want := range []string{tb.msg.EditBtn, tb.msg.CardTagsBtn, tb.msg.DeleteBtn, tb.msg.CardToListBtn} {
		if !markupHasButton(mk, want) {
			t.Errorf("card missing button %q", want)
		}
	}
}

// TestCbCardTagAdd_PromptAndApply: the [➕ Add] button opens a force-reply
// prompt that round-trips through matchTagAddPrompt, and the reply attaches
// the tags.
func TestCbCardTagAdd_PromptAndApply(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "tag me", 0)

	ref := cardRef{id: id, kind: "r", state: recentState{Limit: recentPageSize}.encode()}
	fc := &fakeCtx{callback: &tele.Callback{Data: ref.encode()}}
	if err := tb.cbCardTagAdd(fc); err != nil {
		t.Fatalf("cbCardTagAdd: %v", err)
	}
	sent, ok := fc.lastSent()
	if !ok {
		t.Fatal("expected a force-reply prompt")
	}
	if gotID, ok := tb.matchTagAddPrompt(sent.What.(string)); !ok || gotID != id {
		t.Fatalf("matchTagAddPrompt = (%d,%v), want (%d,true)", gotID, ok, id)
	}

	fc2 := &fakeCtx{}
	if err := tb.handleTagAddReply(fc2, id, "идея философия идея"); err != nil {
		t.Fatalf("handleTagAddReply: %v", err)
	}
	if tags, _ := tb.db.TagsForNote(ctx, id); len(tags) != 2 {
		t.Errorf("want 2 tags (dup ignored), got %v", tags)
	}
	// The reply returns to the tags sub-view, not a dangling "Added N" line.
	sent2, ok := fc2.lastSent()
	if !ok {
		t.Fatal("tag add should reply")
	}
	mk := markupOf(t, sent2.Opts)
	if !markupHasButton(mk, tb.msg.VocabAddBtn) || !markupHasButton(mk, tb.msg.CardBackBtn) {
		t.Error("tag add should re-render the tags menu (➕ Add / ⬅ Back)")
	}
}

// TestCbCardTagRemove removes a tag by its position in the (stable) list.
func TestCbCardTagRemove(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "x", 0)
	if _, err := tb.db.AddTags(ctx, id, []string{"идея", "todo"}); err != nil {
		t.Fatalf("seed tags: %v", err)
	}

	ref := cardRef{id: id, kind: "p", state: pendingState{Limit: pendingPageSize}.encode()}
	fc := &fakeCtx{callback: &tele.Callback{Data: ref.encodeTagRemove(0)}}
	if err := tb.cbCardTagRemove(fc); err != nil {
		t.Fatalf("cbCardTagRemove: %v", err)
	}
	if tags, _ := tb.db.TagsForNote(ctx, id); len(tags) != 1 {
		t.Errorf("want 1 tag after remove, got %v", tags)
	}
}

// TestCbCardDeleteYes_DeletesAndReturnsToList confirms the card's delete path
// erases the note and edits back to the originating list.
func TestCbCardDeleteYes_DeletesAndReturnsToList(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id := seedNoteAt(t, tb, time.Now(), "delete from card")

	ref := cardRef{id: id, kind: "p", state: pendingState{Limit: pendingPageSize}.encode()}
	fc := &fakeCtx{callback: &tele.Callback{Data: ref.encode()}}
	if err := tb.cbCardDeleteYes(fc); err != nil {
		t.Fatalf("cbCardDeleteYes: %v", err)
	}
	if _, err := tb.db.GetNote(ctx, id); err == nil {
		t.Error("note should be deleted")
	}
	if _, ok := fc.lastEdit(); !ok {
		t.Error("expected an Edit (back to the list)")
	}
}
