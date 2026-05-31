package telegram

import (
	"context"
	"testing"

	tele "gopkg.in/telebot.v3"
)

// TestSaveTextNote_InsertsAndReplies covers typed-note capture: a plain text
// message becomes a note (duration 0, no audio) and the user gets the same
// saved-reply + delete markup a voice note produces.
func TestSaveTextNote_InsertsAndReplies(t *testing.T) {
	tb := newTestBot(t)
	fc := &fakeCtx{message: &tele.Message{Text: "  купить молоко завтра  "}}

	if err := tb.saveTextNote(fc, fc.message); err != nil {
		t.Fatalf("saveTextNote: %v", err)
	}

	notes, err := tb.db.ListPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("want 1 note, got %d", len(notes))
	}
	if notes[0].RawText != "купить молоко завтра" {
		t.Fatalf("text not trimmed/stored correctly: %q", notes[0].RawText)
	}
	if notes[0].DurationSec.Valid && notes[0].DurationSec.Int64 != 0 {
		t.Fatalf("text note should have 0 duration, got %d", notes[0].DurationSec.Int64)
	}

	sent, ok := fc.lastSent()
	if !ok {
		t.Fatal("expected a saved-reply Send")
	}
	if len(sent.Opts) == 0 {
		t.Fatal("saved-reply should carry the delete inline markup")
	}
	if _, isMarkup := sent.Opts[0].(*tele.ReplyMarkup); !isMarkup {
		t.Fatalf("expected *tele.ReplyMarkup option, got %T", sent.Opts[0])
	}
}

// TestSaveTextNote_EmptyIgnored: whitespace-only text must not create a note
// or send anything.
func TestSaveTextNote_EmptyIgnored(t *testing.T) {
	tb := newTestBot(t)
	fc := &fakeCtx{message: &tele.Message{Text: "   "}}

	if err := tb.saveTextNote(fc, fc.message); err != nil {
		t.Fatalf("saveTextNote: %v", err)
	}
	notes, err := tb.db.ListPending(context.Background(), 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("want 0 notes for empty text, got %d", len(notes))
	}
	if _, ok := fc.lastSent(); ok {
		t.Fatal("empty text must not send a reply")
	}
}
