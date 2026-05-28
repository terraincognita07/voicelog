package telegram

import (
	"context"
	"strconv"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"

	"github.com/terraincognita07/voicelog/internal/db"
)

// --- saved-reply callbacks (saved_reply.go) ------------------------------
//
// These exercise the [🗑 Discard] / [↩ Restore] / [📖 Show full] buttons
// attached to the bot's voice-recorded reply. Each test seeds a note,
// fires the callback with cb.Data set to the note's id, then asserts
// behavior — DB state flips and the message gets edited in place.

func cbForID(id int64) *tele.Callback {
	return &tele.Callback{Data: strconv.FormatInt(id, 10)}
}

// TestCbDiscard_FlipsStatusAndEditsMessage covers the green path: the
// note's status goes pending→discarded, the edited message shows the
// DiscardedReply body, and Respond is called once to close the spinner.
func TestCbDiscard_FlipsStatusAndEditsMessage(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, err := tb.db.InsertNote(ctx, "to be discarded", 5)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	fc := &fakeCtx{callback: cbForID(id)}
	if err := tb.cbDiscard(fc); err != nil {
		t.Fatalf("cbDiscard: %v", err)
	}

	got, _ := tb.db.GetNote(ctx, id)
	if got.Status != db.StatusDiscarded {
		t.Errorf("status = %q, want discarded", got.Status)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an Edit call")
	}
	body, _ := ed.What.(string)
	if !strings.Contains(body, "to be discarded") {
		t.Errorf("edit body should contain note text: %q", body)
	}
	if len(ed.Opts) == 0 {
		t.Errorf("edit must carry replacement markup for the [↩ Restore] flip")
	}
	if fc.respondCalls != 1 {
		t.Errorf("Respond calls = %d, want 1 (close the spinner)", fc.respondCalls)
	}
}

// TestCbDiscard_BadIDDoesNotTouchDB asserts that garbled callback data
// (non-numeric id) is rejected via Respond with a BadID toast and does
// not mutate any rows.
func TestCbDiscard_BadIDDoesNotTouchDB(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	if _, err := tb.db.InsertNote(ctx, "keep me", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}

	fc := &fakeCtx{callback: &tele.Callback{Data: "not-a-number"}}
	if err := tb.cbDiscard(fc); err != nil {
		t.Fatalf("cbDiscard: %v", err)
	}

	notes := allNotes(t, tb.db)
	if notes[0].Status != db.StatusPending {
		t.Errorf("status mutated on bad-id callback: %q", notes[0].Status)
	}
	if fc.respondCalls != 1 {
		t.Fatalf("Respond calls = %d, want 1 (BadID toast)", fc.respondCalls)
	}
	if len(fc.responses) != 1 || fc.responses[0] == nil || fc.responses[0].Text == "" {
		t.Errorf("BadID Respond must carry a non-empty text: %+v", fc.responses)
	}
}

// TestCbDiscard_NotFoundIsHandled exercises the "id valid but row gone"
// path — the callback edits the message to NotFound and answers the
// callback without surfacing the raw ErrNoteNotFound.
func TestCbDiscard_NotFoundIsHandled(t *testing.T) {
	tb := newTestBot(t)
	fc := &fakeCtx{callback: cbForID(99999)}
	if err := tb.cbDiscard(fc); err != nil {
		t.Fatalf("cbDiscard: %v", err)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an Edit call")
	}
	want := tb.msg.NotFound(99999)
	if ed.What != want {
		t.Errorf("edit body = %q, want %q", ed.What, want)
	}
}

// TestCbSavedRestore_FlipsBack asserts the inverse: a discarded note is
// flipped back to pending and the message body becomes RestoredReply.
func TestCbSavedRestore_FlipsBack(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "restore me", 4)
	if err := tb.db.MarkDiscarded(ctx, id); err != nil {
		t.Fatalf("discard: %v", err)
	}

	fc := &fakeCtx{callback: cbForID(id)}
	if err := tb.cbSavedRestore(fc); err != nil {
		t.Fatalf("cbSavedRestore: %v", err)
	}

	got, _ := tb.db.GetNote(ctx, id)
	if got.Status != db.StatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	ed, _ := fc.lastEdit()
	if !strings.Contains(ed.What.(string), "restore me") {
		t.Errorf("edit body should contain restored note text: %q", ed.What)
	}
}

// TestCbSavedFull_ExpandsTruncated checks the [📖 Show full] callback.
// Callback data is "id:discardedFlag"; the bot fetches the full text and
// edits the message accordingly.
func TestCbSavedFull_ExpandsTruncated(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	long := strings.Repeat("x", 400)
	id, _ := tb.db.InsertNote(ctx, long, 9)

	fc := &fakeCtx{callback: &tele.Callback{Data: strconv.FormatInt(id, 10) + ":0"}}
	if err := tb.cbSavedFull(fc); err != nil {
		t.Fatalf("cbSavedFull: %v", err)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an Edit call")
	}
	body, _ := ed.What.(string)
	if !strings.Contains(body, long) {
		t.Errorf("expanded body should contain the full text (%d runes); got %d chars", len(long), len(body))
	}
}

// TestCbSavedFull_BadIDIsRejected — same defensive check as cbDiscard.
func TestCbSavedFull_BadIDIsRejected(t *testing.T) {
	tb := newTestBot(t)
	fc := &fakeCtx{callback: &tele.Callback{Data: "garbage:0"}}
	if err := tb.cbSavedFull(fc); err != nil {
		t.Fatalf("cbSavedFull: %v", err)
	}
	if fc.respondCalls != 1 {
		t.Fatalf("Respond calls = %d, want 1", fc.respondCalls)
	}
}

// --- vocab callbacks (vocab.go) ------------------------------------------

// TestCbVocabRemove_RemovesAndRerenders ensures a remove-callback wipes
// the term and re-renders the vocab list with the remaining terms.
func TestCbVocabRemove_RemovesAndRerenders(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	for _, term := range []string{"alpha", "beta"} {
		if _, err := tb.db.AddVocab(ctx, term); err != nil {
			t.Fatalf("seed vocab: %v", err)
		}
	}

	fc := &fakeCtx{callback: &tele.Callback{Data: "alpha"}}
	if err := tb.cbVocabRemove(fc); err != nil {
		t.Fatalf("cbVocabRemove: %v", err)
	}

	remaining, _ := tb.db.ListVocab(ctx)
	if len(remaining) != 1 || remaining[0] != "beta" {
		t.Errorf("vocab after remove = %v; want [beta]", remaining)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an Edit call (re-render of vocab list)")
	}
	if !strings.Contains(ed.What.(string), tb.msg.VocabHeader(1)) {
		t.Errorf("re-render body must reflect the new count: %q", ed.What)
	}
}

// TestCbVocabRemove_EmptyDataIsNoOp covers the defensive `term==""` branch.
func TestCbVocabRemove_EmptyDataIsNoOp(t *testing.T) {
	tb := newTestBot(t)
	fc := &fakeCtx{callback: &tele.Callback{Data: "   "}}
	if err := tb.cbVocabRemove(fc); err != nil {
		t.Fatalf("cbVocabRemove: %v", err)
	}
	if len(fc.edited) != 0 {
		t.Errorf("empty-term callback must not Edit; got %d edits", len(fc.edited))
	}
}

// TestCbVocabAddPrompt_SendsForceReply asserts the add-prompt callback
// sends the ForceReply message that onText will later detect.
func TestCbVocabAddPrompt_SendsForceReply(t *testing.T) {
	tb := newTestBot(t)
	fc := &fakeCtx{callback: &tele.Callback{}}
	if err := tb.cbVocabAddPrompt(fc); err != nil {
		t.Fatalf("cbVocabAddPrompt: %v", err)
	}
	if len(fc.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(fc.sent))
	}
	body, _ := fc.sent[0].What.(string)
	if body != tb.msg.VocabAddPrompt {
		t.Errorf("prompt body = %q, want %q", body, tb.msg.VocabAddPrompt)
	}
	// Markup must include ForceReply: that's the whole point.
	if len(fc.sent[0].Opts) != 1 {
		t.Fatalf("expected 1 markup opt (ForceReply), got %d", len(fc.sent[0].Opts))
	}
	rm, ok := fc.sent[0].Opts[0].(*tele.ReplyMarkup)
	if !ok || !rm.ForceReply {
		t.Errorf("opt is not a *ReplyMarkup with ForceReply: %+v", fc.sent[0].Opts[0])
	}
}

// TestCbVocabClearAsk_EditsToConfirmation verifies the [🗑 Clear] callback
// edits the current message into the Yes/No confirm prompt.
func TestCbVocabClearAsk_EditsToConfirmation(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	for _, term := range []string{"a", "b", "c"} {
		_, _ = tb.db.AddVocab(ctx, term)
	}

	fc := &fakeCtx{callback: &tele.Callback{}}
	if err := tb.cbVocabClearAsk(fc); err != nil {
		t.Fatalf("cbVocabClearAsk: %v", err)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an Edit call (confirm prompt)")
	}
	want := tb.msg.VocabClearAsk(3)
	if ed.What != want {
		t.Errorf("confirm body = %q, want %q", ed.What, want)
	}
}

// TestCbVocabClearYes_WipesAndRerenders fires the [Yes] button and asserts
// the entire vocab table is gone, replaced by the EmptyVocab body.
func TestCbVocabClearYes_WipesAndRerenders(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	for _, term := range []string{"x", "y", "z"} {
		_, _ = tb.db.AddVocab(ctx, term)
	}

	fc := &fakeCtx{callback: &tele.Callback{}}
	if err := tb.cbVocabClearYes(fc); err != nil {
		t.Fatalf("cbVocabClearYes: %v", err)
	}

	remaining, _ := tb.db.ListVocab(ctx)
	if len(remaining) != 0 {
		t.Errorf("vocab after clear = %v, want empty", remaining)
	}
	ed, _ := fc.lastEdit()
	if ed.What != tb.msg.EmptyVocab {
		t.Errorf("post-clear body = %q, want EmptyVocab (%q)", ed.What, tb.msg.EmptyVocab)
	}
}

// TestCbVocabClearNo_LeavesVocabIntact fires [No] and asserts the table is
// unchanged AND the view is re-rendered with the surviving terms.
func TestCbVocabClearNo_LeavesVocabIntact(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	for _, term := range []string{"keep1", "keep2"} {
		_, _ = tb.db.AddVocab(ctx, term)
	}

	fc := &fakeCtx{callback: &tele.Callback{}}
	if err := tb.cbVocabClearNo(fc); err != nil {
		t.Fatalf("cbVocabClearNo: %v", err)
	}
	remaining, _ := tb.db.ListVocab(ctx)
	if len(remaining) != 2 {
		t.Errorf("vocab after cancel = %v, want 2 terms unchanged", remaining)
	}
	ed, _ := fc.lastEdit()
	body, _ := ed.What.(string)
	if !strings.Contains(body, tb.msg.VocabHeader(2)) {
		t.Errorf("re-render body must show 2-term header: %q", body)
	}
}
