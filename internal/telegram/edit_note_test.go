package telegram

import (
	"context"
	"strconv"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestEditOriginRoundTrip(t *testing.T) {
	cases := []struct {
		data  string
		id    int64
		kind  string
		state string
		ok    bool
	}{
		{"7", 7, "", "", true},           // saved-reply origin: bare id
		{"7:r:", 7, "r", "", true},       // card origin, no list state
		{"7:p:f~2", 7, "p", "f~2", true}, // card origin with list state
		{"garbage", 0, "", "", false},
		{"0", 0, "", "", false}, // non-positive id rejected
		{"  ", 0, "", "", false},
	}
	for _, c := range cases {
		id, kind, state, ok := editOriginFromData(c.data)
		if ok != c.ok || id != c.id || kind != c.kind || state != c.state {
			t.Errorf("editOriginFromData(%q) = (%d,%q,%q,%v), want (%d,%q,%q,%v)",
				c.data, id, kind, state, ok, c.id, c.kind, c.state, c.ok)
		}
		if c.ok {
			if got := editOriginData(c.id, c.kind, c.state); got != c.data {
				t.Errorf("editOriginData(%d,%q,%q) = %q, want %q", c.id, c.kind, c.state, got, c.data)
			}
		}
	}
}

func TestCbEditOpen_ShowsMenuInPlace(t *testing.T) {
	tb := newTestBot(t)
	id, _ := tb.db.InsertNote(context.Background(), "позвонить Коле завтра", 0)
	fc := &fakeCtx{callback: &tele.Callback{Data: strconv.FormatInt(id, 10)}}

	if err := tb.cbEditOpen(fc); err != nil {
		t.Fatalf("cbEditOpen: %v", err)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected the message to be edited in place")
	}
	if _, sent := fc.lastSent(); sent {
		t.Error("cbEditOpen must edit in place, not Send a new (dangling) message")
	}
	mk := markupOf(t, ed.Opts)
	for _, want := range []string{tb.msg.EditReplaceBtn, tb.msg.EditFullBtn, tb.msg.DeleteNoBtn} {
		if !markupHasButton(mk, want) {
			t.Errorf("edit menu missing button %q", want)
		}
	}
}

func TestCbEditReplace_ArmsFindState(t *testing.T) {
	tb := newTestBot(t)
	id, _ := tb.db.InsertNote(context.Background(), "позвонить Коле завтра", 0)
	fc := &fakeCtx{callback: &tele.Callback{Data: strconv.FormatInt(id, 10)}}

	if err := tb.cbEditReplace(fc); err != nil {
		t.Fatalf("cbEditReplace: %v", err)
	}
	pe := tb.currentEditState()
	if pe == nil || pe.noteID != id || pe.step != editAwaitFind {
		t.Fatalf("expected armed await-find state for note %d, got %+v", id, pe)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an in-place edit to the find prompt")
	}
	if mk := markupOf(t, ed.Opts); !markupHasButton(mk, tb.msg.DeleteNoBtn) {
		t.Error("the find prompt should offer ✗ Cancel")
	}
}

func TestAdvanceEdit_FullRewrite(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "оригинальный текст", 0)
	pe := &pendingEdit{noteID: id, step: editAwaitFull}

	_, kb, done, err := tb.advanceEdit(ctx, pe, "  исправленный текст  ")
	if err != nil {
		t.Fatalf("advanceEdit: %v", err)
	}
	if !done {
		t.Error("a full rewrite should finish the flow")
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "исправленный текст" {
		t.Fatalf("text not updated/trimmed: %q", n.RawText)
	}
	assertArchived(t, tb, id, "оригинальный текст")
	if !markupHasButton(kb, tb.msg.EditBtn) {
		t.Error("restored view should carry the saved-reply 🗑/✏️ markup")
	}
}

func TestAdvanceEdit_ReplaceFlow(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "позвонить Коле завтра", 0)
	pe := &pendingEdit{noteID: id, step: editAwaitFind}

	// Step 1: which word to replace.
	body, _, done, err := tb.advanceEdit(ctx, pe, "Коле")
	if err != nil {
		t.Fatalf("advanceEdit (find): %v", err)
	}
	if done {
		t.Error("after the find word the flow should continue to the replacement")
	}
	if pe.step != editAwaitRepl || pe.find != "Коле" {
		t.Fatalf("state not advanced to await-replacement: %+v", pe)
	}
	if !strings.Contains(body, "Коле") {
		t.Errorf("replacement prompt should name the word, got %q", body)
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "позвонить Коле завтра" {
		t.Fatalf("note must not change until the replacement is given: %q", n.RawText)
	}

	// Step 2: replace with what.
	_, _, done, err = tb.advanceEdit(ctx, pe, "Оле")
	if err != nil {
		t.Fatalf("advanceEdit (repl): %v", err)
	}
	if !done {
		t.Error("after the replacement the flow should finish")
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "позвонить Оле завтра" {
		t.Fatalf("replace failed: %q", n.RawText)
	}
	assertArchived(t, tb, id, "позвонить Коле завтра")
}

func TestAdvanceEdit_FindNotInNote(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "исходный текст", 0)
	pe := &pendingEdit{noteID: id, step: editAwaitFind}

	body, kb, done, err := tb.advanceEdit(ctx, pe, "отсутствует")
	if err != nil {
		t.Fatalf("advanceEdit: %v", err)
	}
	if !done {
		t.Error("a missing word should end the step")
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "исходный текст" {
		t.Errorf("note must not change when the word isn't found: %q", n.RawText)
	}
	if !strings.Contains(body, "отсутствует") {
		t.Errorf("should name the missing word, got %q", body)
	}
	if !markupHasButton(kb, tb.msg.EditReplaceBtn) {
		t.Error("should fall back to the edit menu so the user can retry or cancel")
	}
}

func TestContinueEdit_AppliesAndClears(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "старый", 0)
	pe := &pendingEdit{noteID: id, step: editAwaitFull}
	tb.setEditState(pe)

	// tb.bot is nil in tests, so the in-place edit / message delete are no-ops;
	// the DB mutation and state cleanup are what we assert.
	if err := tb.continueEdit(&tele.Message{Text: "новый"}, pe); err != nil {
		t.Fatalf("continueEdit: %v", err)
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "новый" {
		t.Fatalf("text not updated: %q", n.RawText)
	}
	if tb.currentEditState() != nil {
		t.Error("edit state must be cleared once the flow is done")
	}
}

func TestCbEditCancel_RestoresAndClears(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "не трогать", 0)
	tb.setEditState(&pendingEdit{noteID: id, step: editAwaitFind})
	fc := &fakeCtx{callback: &tele.Callback{Data: strconv.FormatInt(id, 10)}}

	if err := tb.cbEditCancel(fc); err != nil {
		t.Fatalf("cbEditCancel: %v", err)
	}
	if tb.currentEditState() != nil {
		t.Error("cancel must clear the edit state")
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("cancel should restore the note in place")
	}
	if mk := markupOf(t, ed.Opts); !markupHasButton(mk, tb.msg.EditBtn) {
		t.Error("restored saved-reply should offer ✏️ again")
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "не трогать" {
		t.Errorf("cancel must not change the note: %q", n.RawText)
	}
}

func TestSavedMarkup_OffersDeleteAndEdit(t *testing.T) {
	tb := newTestBot(t)

	mk := tb.savedMarkup(5, false)
	if !markupHasButton(mk, tb.msg.DeleteBtn) {
		t.Error("saved note should offer the Delete button")
	}
	if !markupHasButton(mk, tb.msg.EditBtn) {
		t.Error("saved note should offer the Edit button")
	}
}

// --- helpers --------------------------------------------------------------

func markupOf(t *testing.T, opts []interface{}) *tele.ReplyMarkup {
	t.Helper()
	if len(opts) == 0 {
		t.Fatal("no reply markup in opts")
	}
	mk, ok := opts[0].(*tele.ReplyMarkup)
	if !ok {
		t.Fatalf("expected *tele.ReplyMarkup, got %T", opts[0])
	}
	return mk
}

func markupHasButton(mk *tele.ReplyMarkup, text string) bool {
	for _, row := range mk.InlineKeyboard {
		for _, b := range row {
			if b.Text == text {
				return true
			}
		}
	}
	return false
}

func assertArchived(t *testing.T, tb *Bot, id int64, want string) {
	t.Helper()
	var got string
	if err := tb.db.QueryRowContext(context.Background(),
		`SELECT raw_text FROM notes_history WHERE note_id = ? ORDER BY id DESC LIMIT 1`, id).Scan(&got); err != nil {
		t.Fatalf("history lookup: %v", err)
	}
	if got != want {
		t.Fatalf("archived text = %q, want %q", got, want)
	}
}
