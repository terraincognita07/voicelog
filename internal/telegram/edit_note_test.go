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

func TestReplaceNth(t *testing.T) {
	cases := []struct {
		s, old, neu string
		n           int
		want        string
		ok          bool
	}{
		{"кот и ещё кот", "кот", "пёс", 0, "пёс и ещё кот", true},
		{"кот и ещё кот", "кот", "пёс", 1, "кот и ещё пёс", true},
		{"кот и ещё кот", "кот", "пёс", 2, "кот и ещё кот", false}, // only two occurrences
		{"abcabcabc", "abc", "X", 1, "abcXabc", true},
		{"nothing here", "zzz", "X", 0, "nothing here", false},
		{"x", "", "y", 0, "x", false}, // empty find never matches
	}
	for _, c := range cases {
		got, ok := replaceNth(c.s, c.old, c.neu, c.n)
		if got != c.want || ok != c.ok {
			t.Errorf("replaceNth(%q,%q,%q,%d) = (%q,%v), want (%q,%v)",
				c.s, c.old, c.neu, c.n, got, ok, c.want, c.ok)
		}
	}
}

func TestEditPickDataRoundTrip(t *testing.T) {
	cases := []struct {
		idx   int
		id    int64
		kind  string
		state string
	}{
		{-1, 7, "", ""},          // saved-reply origin, "all"
		{0, 7, "", ""},           // saved-reply origin, first occurrence
		{2, 12, "p", "f~2"},      // card origin with list state
		{5, 9999999999, "r", ""}, // card origin, large id
	}
	for _, c := range cases {
		data := editPickData(c.idx, c.id, c.kind, c.state)
		if len(data) > 64 {
			t.Errorf("editPickData %q exceeds the 64-byte callback budget (%d)", data, len(data))
		}
		idx, id, kind, state, ok := editPickFromData(data)
		if !ok || idx != c.idx || id != c.id || kind != c.kind || state != c.state {
			t.Errorf("round-trip %q = (%d,%d,%q,%q,%v), want (%d,%d,%q,%q,true)",
				data, idx, id, kind, state, ok, c.idx, c.id, c.kind, c.state)
		}
	}
}

func TestAdvanceEdit_ReplacePicker_ShowsOccurrences(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "кот и ещё кот", 0)
	pe := &pendingEdit{noteID: id, step: editAwaitFind}

	// An ambiguous find (>1 match) opens the picker instead of replacing all.
	body, kb, done, err := tb.advanceEdit(ctx, pe, "кот")
	if err != nil {
		t.Fatalf("advanceEdit (find): %v", err)
	}
	if done {
		t.Error("an ambiguous find should open the picker, not finish")
	}
	if pe.step != editAwaitPick {
		t.Fatalf("expected step editAwaitPick, got %d", pe.step)
	}
	if !strings.Contains(body, "кот") {
		t.Errorf("picker body should quote the matches, got %q", body)
	}
	if !markupHasButton(kb, "1") || !markupHasButton(kb, "2") {
		t.Error("picker should offer a numbered button per occurrence")
	}
	if !markupHasButton(kb, tb.msg.EditPickAllBtn) {
		t.Error("picker should offer Replace all")
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "кот и ещё кот" {
		t.Errorf("note must not change while picking: %q", n.RawText)
	}
}

func TestAdvanceEdit_ReplacePickAll(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "кот и ещё кот", 0)
	// Past the picker: "Replace all" chosen (pickIdx -1), awaiting the text.
	pe := &pendingEdit{noteID: id, step: editAwaitRepl, find: "кот", pickIdx: -1}

	_, _, done, err := tb.advanceEdit(ctx, pe, "пёс")
	if err != nil {
		t.Fatalf("advanceEdit (repl): %v", err)
	}
	if !done {
		t.Error("the replacement should finish the flow")
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "пёс и ещё пёс" {
		t.Fatalf("Replace all did not rewrite every occurrence: %q", n.RawText)
	}
}

func TestAdvanceEdit_ReplacePickOne(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "кот и ещё кот", 0)
	// Second occurrence picked (0-based index 1) — only it must change.
	pe := &pendingEdit{noteID: id, step: editAwaitRepl, find: "кот", pickIdx: 1}

	_, _, done, err := tb.advanceEdit(ctx, pe, "пёс")
	if err != nil {
		t.Fatalf("advanceEdit (repl): %v", err)
	}
	if !done {
		t.Error("the replacement should finish the flow")
	}
	if n, _ := tb.db.GetNote(ctx, id); n.RawText != "кот и ещё пёс" {
		t.Fatalf("only the chosen occurrence should change: %q", n.RawText)
	}
	assertArchived(t, tb, id, "кот и ещё кот")
}

func TestCbEditPick_ArmsReplaceForChosenOccurrence(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "кот и ещё кот", 0)
	tb.setEditState(&pendingEdit{noteID: id, step: editAwaitPick, find: "кот", pickIdx: -1})

	fc := &fakeCtx{callback: &tele.Callback{Data: editPickData(1, id, "", "")}}
	if err := tb.cbEditPick(fc); err != nil {
		t.Fatalf("cbEditPick: %v", err)
	}
	pe := tb.currentEditState()
	if pe == nil || pe.step != editAwaitRepl || pe.pickIdx != 1 {
		t.Fatalf("cbEditPick should arm await-repl for occurrence 1, got %+v", pe)
	}
	if _, ok := fc.lastEdit(); !ok {
		t.Error("cbEditPick should edit the menu into the replacement prompt")
	}
}

func TestCbCardTagAdd_ClearsHalfFinishedEdit(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "позвонить Коле", 0)
	// A replace edit left half-finished (awaiting the replacement word) is
	// exactly what used to leak into the next message as a phantom edit.
	tb.setEditState(&pendingEdit{noteID: id, step: editAwaitRepl, find: "Коле"})

	ref := cardRef{id: id, kind: "p"}
	fc := &fakeCtx{callback: &tele.Callback{Data: ref.encode()}}
	if err := tb.cbCardTagAdd(fc); err != nil {
		t.Fatalf("cbCardTagAdd: %v", err)
	}
	if tb.currentEditState() != nil {
		t.Error("opening the tag-add prompt must abandon the half-finished edit")
	}
	if _, sent := fc.lastSent(); !sent {
		t.Error("cbCardTagAdd should send the tag force-reply prompt")
	}
}

func TestCbDeleteAsk_ClearsHalfFinishedEdit(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "позвонить Коле", 0)
	tb.setEditState(&pendingEdit{noteID: id, step: editAwaitRepl, find: "Коле"})

	fc := &fakeCtx{callback: &tele.Callback{Data: strconv.FormatInt(id, 10)}}
	if err := tb.cbDeleteAsk(fc); err != nil {
		t.Fatalf("cbDeleteAsk: %v", err)
	}
	if tb.currentEditState() != nil {
		t.Error("asking to delete must abandon the half-finished edit")
	}
}

func TestCbCardDeleteAsk_ClearsHalfFinishedEdit(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "позвонить Коле", 0)
	tb.setEditState(&pendingEdit{noteID: id, step: editAwaitFind})

	ref := cardRef{id: id, kind: "p"}
	fc := &fakeCtx{callback: &tele.Callback{Data: ref.encode()}}
	if err := tb.cbCardDeleteAsk(fc); err != nil {
		t.Fatalf("cbCardDeleteAsk: %v", err)
	}
	if tb.currentEditState() != nil {
		t.Error("asking to delete (card) must abandon the half-finished edit")
	}
}

func TestRenderEditMenu_ShowFullExpands(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	long := strings.Repeat("слово ", 60) // 360 runes > savedPreviewLen
	id, _ := tb.db.InsertNote(ctx, long, 0)

	// Compact: clipped preview + a 📖 Show full button.
	body, kb, err := tb.renderEditMenu(ctx, id, "", "", "", false)
	if err != nil {
		t.Fatalf("renderEditMenu compact: %v", err)
	}
	if !strings.Contains(body, "…") {
		t.Error("compact menu should clip a long note with an ellipsis")
	}
	if !markupHasButton(kb, tb.msg.ShowFullBtn) {
		t.Error("compact menu of a long note should offer 📖 Show full")
	}

	// Expanded: full text, no Show-full button, edit actions intact.
	body, kb, err = tb.renderEditMenu(ctx, id, "", "", "", true)
	if err != nil {
		t.Fatalf("renderEditMenu expanded: %v", err)
	}
	if strings.Contains(body, "…") {
		t.Error("expanded menu should show the full text without an ellipsis")
	}
	if markupHasButton(kb, tb.msg.ShowFullBtn) {
		t.Error("expanded menu should drop the Show-full button")
	}
	if !markupHasButton(kb, tb.msg.EditReplaceBtn) || !markupHasButton(kb, tb.msg.EditFullBtn) {
		t.Error("expanded menu should still offer the edit actions")
	}
}

func TestRenderEditMenu_ShortNoteHasNoExpand(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, "коротко", 0)

	_, kb, err := tb.renderEditMenu(ctx, id, "", "", "", false)
	if err != nil {
		t.Fatalf("renderEditMenu: %v", err)
	}
	if markupHasButton(kb, tb.msg.ShowFullBtn) {
		t.Error("a short note that already fits should not offer Show full")
	}
}

func TestCbEditExpand_ShowsFullInPlace(t *testing.T) {
	tb := newTestBot(t)
	ctx := context.Background()
	id, _ := tb.db.InsertNote(ctx, strings.Repeat("слово ", 60), 0)
	fc := &fakeCtx{callback: &tele.Callback{Data: strconv.FormatInt(id, 10)}}

	if err := tb.cbEditExpand(fc); err != nil {
		t.Fatalf("cbEditExpand: %v", err)
	}
	ed, ok := fc.lastEdit()
	if !ok {
		t.Fatal("expand should edit the menu in place, not send a new message")
	}
	if mk := markupOf(t, ed.Opts); markupHasButton(mk, tb.msg.ShowFullBtn) {
		t.Error("after expand the Show-full button should be gone")
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
