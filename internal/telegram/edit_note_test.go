package telegram

import (
	"context"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestMatchEditPrompt(t *testing.T) {
	tb := newTestBot(t)

	if id, ok := tb.matchEditPrompt(tb.msg.EditPrompt(42)); !ok || id != 42 {
		t.Errorf("round-trip prompt should match id 42, got (%d, %v)", id, ok)
	}
	if _, ok := tb.matchEditPrompt("just some unrelated text"); ok {
		t.Error("non-prompt text must not match")
	}
	// A string that contains a number but is NOT the rendered prompt.
	if _, ok := tb.matchEditPrompt("note 42 something else entirely"); ok {
		t.Error("text with a number but wrong shape must not match")
	}
}

func TestApplyNoteEdit_UpdatesAndArchives(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, err := tb.db.InsertNote(ctx, "оригинальный текст", 0)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	fc := &fakeCtx{}
	if err := tb.applyNoteEdit(fc, id, "  исправленный текст  "); err != nil {
		t.Fatalf("applyNoteEdit: %v", err)
	}

	n, err := tb.db.GetNote(ctx, id)
	if err != nil {
		t.Fatalf("get note: %v", err)
	}
	if n.RawText != "исправленный текст" {
		t.Fatalf("text not updated/trimmed: %q", n.RawText)
	}

	// Old text archived to notes_history.
	var archived string
	if err := tb.db.QueryRowContext(ctx,
		`SELECT raw_text FROM notes_history WHERE note_id = ? ORDER BY id DESC LIMIT 1`, id).
		Scan(&archived); err != nil {
		t.Fatalf("history lookup: %v", err)
	}
	if archived != "оригинальный текст" {
		t.Fatalf("previous text not archived, got %q", archived)
	}

	sent, ok := fc.lastSent()
	if !ok {
		t.Fatal("expected a confirmation Send")
	}
	if len(sent.Opts) == 0 {
		t.Fatal("confirmation should carry the saved-reply markup")
	}
	if _, isMarkup := sent.Opts[0].(*tele.ReplyMarkup); !isMarkup {
		t.Fatalf("expected *tele.ReplyMarkup, got %T", sent.Opts[0])
	}
}

func TestApplyNoteEdit_EmptyIsNoop(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "оригинал", 0)

	fc := &fakeCtx{}
	if err := tb.applyNoteEdit(fc, id, "   "); err != nil {
		t.Fatalf("applyNoteEdit: %v", err)
	}
	if _, ok := fc.lastSent(); ok {
		t.Error("empty edit must not send anything")
	}
	n, _ := tb.db.GetNote(ctx, id)
	if n.RawText != "оригинал" {
		t.Errorf("empty edit must not change text, got %q", n.RawText)
	}
}

func TestApplyNoteEdit_NotFound(t *testing.T) {
	tb := newTestBot(t)
	fc := &fakeCtx{}
	if err := tb.applyNoteEdit(fc, 99999, "new"); err != nil {
		t.Fatalf("applyNoteEdit returned error (should send NotFound instead): %v", err)
	}
	sent, ok := fc.lastSent()
	if !ok {
		t.Fatal("expected a NotFound reply")
	}
	if !strings.Contains(sent.What.(string), "99999") {
		t.Errorf("NotFound reply should mention the id, got %q", sent.What)
	}
}

func TestSplitEdit(t *testing.T) {
	cases := []struct {
		in, find, repl string
		ok             bool
	}{
		{"a → b", "a", "b", true},
		{"a -> b", "a", "b", true},
		{"a => b", "a", "b", true},
		{"слово→другое", "слово", "другое", true},              // bare arrow, no spaces
		{"x -> y inside -> z", "x", "y inside -> z", true},     // first separator wins
		{"just full corrected text", "", "", false},            // no separator → full replace
		{"arrow in prose x->y stays full text", "", "", false}, // unspaced ASCII isn't a separator
	}
	for _, c := range cases {
		find, repl, ok := splitEdit(c.in)
		if ok != c.ok || find != c.find || repl != c.repl {
			t.Errorf("splitEdit(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, find, repl, ok, c.find, c.repl, c.ok)
		}
	}
}

func TestApplyNoteEdit_FindReplace(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "позвонить Коле завтра", 0)

	fc := &fakeCtx{}
	if err := tb.applyNoteEdit(fc, id, "Коле → Оле"); err != nil {
		t.Fatalf("applyNoteEdit: %v", err)
	}
	n, _ := tb.db.GetNote(ctx, id)
	if n.RawText != "позвонить Оле завтра" {
		t.Fatalf("find→replace failed: %q", n.RawText)
	}
	var archived string
	_ = tb.db.QueryRowContext(ctx,
		`SELECT raw_text FROM notes_history WHERE note_id = ? ORDER BY id DESC LIMIT 1`, id).Scan(&archived)
	if archived != "позвонить Коле завтра" {
		t.Errorf("original not archived, got %q", archived)
	}
}

func TestApplyNoteEdit_FindReplace_AllOccurrencesAsciiArrow(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "тест тест тест", 0)

	fc := &fakeCtx{}
	if err := tb.applyNoteEdit(fc, id, "тест -> проба"); err != nil {
		t.Fatalf("applyNoteEdit: %v", err)
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "проба проба проба" {
		t.Fatalf("all-occurrences replace failed: %q", n.RawText)
	}
}

func TestApplyNoteEdit_FindNotFound(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "исходный текст", 0)

	fc := &fakeCtx{}
	if err := tb.applyNoteEdit(fc, id, "отсутствует → замена"); err != nil {
		t.Fatalf("applyNoteEdit: %v", err)
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "исходный текст" {
		t.Errorf("not-found edit must not change text, got %q", n.RawText)
	}
	sent, ok := fc.lastSent()
	if !ok {
		t.Fatal("expected a not-found reply")
	}
	if !strings.Contains(sent.What.(string), "отсутствует") {
		t.Errorf("reply should name the missing term, got %q", sent.What)
	}
}

func TestCbEditPrompt_SendsForceReply(t *testing.T) {
	tb := newTestBot(t)
	fc := &fakeCtx{callback: &tele.Callback{Data: "7"}}
	if err := tb.cbEditPrompt(fc); err != nil {
		t.Fatalf("cbEditPrompt: %v", err)
	}
	sent, ok := fc.lastSent()
	if !ok {
		t.Fatal("expected a force-reply prompt Send")
	}
	if !strings.Contains(sent.What.(string), "7") {
		t.Errorf("prompt should mention note id 7, got %q", sent.What)
	}
	if len(sent.Opts) == 0 {
		t.Fatal("prompt must carry a ForceReply markup")
	}
	mk, isMarkup := sent.Opts[0].(*tele.ReplyMarkup)
	if !isMarkup || !mk.ForceReply {
		t.Fatalf("expected ForceReply markup, got %#v", sent.Opts[0])
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
