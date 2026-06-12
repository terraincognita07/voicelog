package telegram

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"

	tele "gopkg.in/telebot.v3"
)

// TestEditFlow_PickVsType_NoFieldRace drives the two flows that mutate an
// in-flight pendingEdit — a picker tap (cbEditPick) and a typed step
// (continueEdit) — concurrently on the same edit, the way telebot's
// per-update goroutines can. Before editFlowMu they wrote pe.step/find/pickIdx
// through the shared pointer at once; `go test -race` (CI's gate) flags that.
// With the lock the two flows serialize and the test is clean. The assertion
// is the race detector itself plus "no panic"; we don't pin an ordering.
func TestEditFlow_PickVsType_NoFieldRace(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()

	for i := 0; i < 200; i++ {
		id, err := tb.db.InsertNote(ctx, "кот и ещё кот", 0)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		tb.setEditState(&pendingEdit{noteID: id, step: editAwaitPick, find: "кот", pickIdx: -1})

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)

		// Flow A: the user taps an occurrence in the picker.
		go func() {
			defer wg.Done()
			fc := &fakeCtx{callback: &tele.Callback{Data: editPickData(1, id, "", "")}}
			<-start
			_ = tb.cbEditPick(fc)
		}()

		// Flow B: a typed message lands for the same edit.
		go func() {
			defer wg.Done()
			pe := tb.currentEditState()
			<-start
			if pe != nil {
				_ = tb.continueEdit(&tele.Message{Text: "собака"}, pe)
			}
		}()

		close(start)
		wg.Wait()
		tb.clearEditState()
	}
}

// TestRedactErr_StripsBotToken proves the logging redactor removes the bot
// token from a telebot-style transport error. telebot puts the token in the
// URL path and Go's *url.Error keeps it verbatim, so an unsanitized log of
// such an error would leak BOT_TOKEN — the exact "never log secrets" invariant.
func TestRedactErr_StripsBotToken(t *testing.T) {
	const token = "123456789:AA-this-is-a-fake-secret-bot-token"
	tb := &Bot{token: token}

	// Shape matches what telebot returns when a Telegram API call fails at the
	// transport layer (see internal/whisper proof: net/http yields *url.Error).
	transportErr := &url.Error{
		Op:  "Post",
		URL: "https://api.telegram.org/bot" + token + "/sendMessage",
		Err: errors.New("dial tcp: connection refused"),
	}

	got := tb.redactErr(transportErr)
	if strings.Contains(got, token) {
		t.Fatalf("redactErr leaked the bot token: %q", got)
	}
	if !strings.Contains(got, "<bot-token>") {
		t.Fatalf("expected the <bot-token> placeholder, got %q", got)
	}

	// nil → empty, and an unrelated error passes through unchanged.
	if s := tb.redactErr(nil); s != "" {
		t.Errorf("redactErr(nil) = %q, want empty", s)
	}
	if s := tb.redactErr(errors.New("plain db error")); s != "plain db error" {
		t.Errorf("redactErr passthrough = %q", s)
	}
}
