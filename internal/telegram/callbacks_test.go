package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	tele "gopkg.in/telebot.v3"

	"github.com/terraincognita07/voicelog/internal/db"
)

// --- saved-reply callbacks (saved_reply.go) ------------------------------
//
// These exercise the [🗑 Delete] confirm flow ([🗑]→Ask→[✓ Yes]/[✗ Cancel])
// and the [📖 Show full] button attached to the bot's saved-note reply.
// Each test seeds a note, fires a callback with cb.Data set to the note's
// id, then asserts behavior — DB state and in-place message edits.

func cbForID(id int64) *tele.Callback {
	return &tele.Callback{Data: strconv.FormatInt(id, 10)}
}

// TestCbDeleteAsk_ShowsConfirmWithoutDeleting: the 🗑 tap must NOT delete —
// it swaps the message to the confirm prompt and keeps the row intact.
func TestCbDeleteAsk_ShowsConfirmWithoutDeleting(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, err := tb.db.InsertNote(ctx, "keep until confirmed", 5)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	fc := &fakeCtx{callback: cbForID(id)}
	if err := tb.cbDeleteAsk(fc); err != nil {
		t.Fatalf("cbDeleteAsk: %v", err)
	}

	if _, err := tb.db.GetNote(ctx, id); err != nil {
		t.Errorf("note must still exist after Ask, got %v", err)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an Edit call (the confirm prompt)")
	}
	if ed.What != tb.msg.DeleteAsk(id) {
		t.Errorf("edit body = %q, want the DeleteAsk prompt", ed.What)
	}
	if len(ed.Opts) == 0 {
		t.Errorf("confirm must carry the [✓ Yes]/[✗ Cancel] markup")
	}
	if fc.respondCalls != 1 {
		t.Errorf("Respond calls = %d, want 1", fc.respondCalls)
	}
}

// TestCbDeleteYes_DeletesAndEdits covers the confirmed path: the row is
// gone and the message shows the Deleted body.
func TestCbDeleteYes_DeletesAndEdits(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, err := tb.db.InsertNote(ctx, "to be deleted", 5)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	fc := &fakeCtx{callback: cbForID(id)}
	if err := tb.cbDeleteYes(fc); err != nil {
		t.Fatalf("cbDeleteYes: %v", err)
	}

	if _, err := tb.db.GetNote(ctx, id); !errors.Is(err, db.ErrNoteNotFound) {
		t.Errorf("note must be gone after Yes, got %v", err)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an Edit call")
	}
	if ed.What != tb.msg.Deleted(id) {
		t.Errorf("edit body = %q, want the Deleted confirmation", ed.What)
	}
	if fc.respondCalls != 1 {
		t.Errorf("Respond calls = %d, want 1", fc.respondCalls)
	}
}

// TestCbDeleteAsk_BadIDDoesNotTouchDB: garbled callback data is rejected
// via a BadID toast and mutates nothing.
func TestCbDeleteAsk_BadIDDoesNotTouchDB(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	if _, err := tb.db.InsertNote(ctx, "keep me", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}

	fc := &fakeCtx{callback: &tele.Callback{Data: "not-a-number"}}
	if err := tb.cbDeleteAsk(fc); err != nil {
		t.Fatalf("cbDeleteAsk: %v", err)
	}

	notes := allNotes(t, tb.db)
	if len(notes) != 1 || notes[0].Status != db.StatusPending {
		t.Errorf("row mutated on bad-id callback: %+v", notes)
	}
	if fc.respondCalls != 1 {
		t.Fatalf("Respond calls = %d, want 1 (BadID toast)", fc.respondCalls)
	}
	if len(fc.responses) != 1 || fc.responses[0] == nil || fc.responses[0].Text == "" {
		t.Errorf("BadID Respond must carry a non-empty text: %+v", fc.responses)
	}
}

// TestCbDeleteYes_NotFoundIsHandled: id valid but row already gone — the
// callback edits the message to NotFound without surfacing the raw error.
func TestCbDeleteYes_NotFoundIsHandled(t *testing.T) {
	tb := newTestBot(t)
	fc := &fakeCtx{callback: cbForID(99999)}
	if err := tb.cbDeleteYes(fc); err != nil {
		t.Fatalf("cbDeleteYes: %v", err)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an Edit call")
	}
	if ed.What != tb.msg.NotFound(99999) {
		t.Errorf("edit body = %q, want NotFound", ed.What)
	}
}

// TestCbDeleteNo_RestoresSavedReply: Cancel re-renders the saved-reply
// (preview + 🗑/✏️ keyboard) and leaves the row untouched.
func TestCbDeleteNo_RestoresSavedReply(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "spared note", 4)

	fc := &fakeCtx{callback: cbForID(id)}
	if err := tb.cbDeleteNo(fc); err != nil {
		t.Fatalf("cbDeleteNo: %v", err)
	}

	if got, err := tb.db.GetNote(ctx, id); err != nil || got.Status != db.StatusPending {
		t.Errorf("note must survive Cancel as pending, got %+v err=%v", got, err)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expected an Edit call (re-render)")
	}
	if !strings.Contains(ed.What.(string), "spared note") {
		t.Errorf("re-render should show the note preview: %q", ed.What)
	}
	if len(ed.Opts) == 0 {
		t.Errorf("re-render must restore the 🗑/✏️ keyboard")
	}
}

// TestCbSavedFull_ExpandsTruncated checks the [📖 Show full] callback.
// Callback data is the bare note id; the bot fetches the full text and
// edits the message accordingly.
func TestCbSavedFull_ExpandsTruncated(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	long := strings.Repeat("x", 400)
	id, _ := tb.db.InsertNote(ctx, long, 9)

	fc := &fakeCtx{callback: cbForID(id)}
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

// TestCbSavedFull_BadIDIsRejected — same defensive check as cbDeleteAsk.
func TestCbSavedFull_BadIDIsRejected(t *testing.T) {
	tb := newTestBot(t)
	fc := &fakeCtx{callback: &tele.Callback{Data: "garbage"}}
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
