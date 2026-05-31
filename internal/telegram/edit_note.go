package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/terraincognita07/voicelog/internal/db"
)

// Button-driven note editing. Tapping [✏️ Edit] (on a saved-note reply or a
// card) turns that same message into an edit menu — no separate "service"
// message is sent, so nothing is left dangling: ✗ Cancel just restores the
// note in place.
//
//	✏️ Note #15 — what do you want to change?
//	«позвонить Коле завтра»
//	[🔤 Replace a word] [📝 Rewrite all]
//	[✗ Cancel]
//
// 🔤 asks which word → with what; when that word matches more than once an
// occurrence picker (editAwaitPick) lets the user replace just one or all.
// 📝 asks for the whole new text. The typed answers are caught by continueEdit
// (via onText's editState check), applied through db.ArchiveAndUpdateText
// (previous text kept in notes_history), and the note message is updated in
// place. The user's typed messages are deleted so the chat stays clean.
//
// State is a single in-memory slot on the Bot (the bot is single-user); a
// second ✏️ supersedes any half-finished edit, and a bot restart simply drops
// the in-flight edit (the buttons stay, the next tap re-arms it).

var (
	editReplaceBtn = tele.InlineButton{Unique: "edit_repl"}   // 🔤 replace a word/phrase
	editFullBtn    = tele.InlineButton{Unique: "edit_full"}   // 📝 rewrite the whole text
	editCancelBtn  = tele.InlineButton{Unique: "edit_cancel"} // ✗ cancel, restore the note
	editPickBtn    = tele.InlineButton{Unique: "edit_pick"}   // ‹n›/all occurrence picker for Replace
)

// editStep marks which typed answer the flow is waiting for.
type editStep int

const (
	editAwaitFull editStep = iota + 1 // the whole new text
	editAwaitFind                     // the word/phrase to find
	editAwaitPick                     // which occurrence (or all) to replace, when find matches >1
	editAwaitRepl                     // what to replace `find` with
)

// pendingEdit is the in-flight button-driven edit. menuMsg is the note message
// being rewritten in place (set once a typed answer is awaited); find holds
// the captured "old" term while step == editAwaitRepl.
type pendingEdit struct {
	noteID  int64
	kind    string // "" = saved-reply origin; "p"/"r" = card origin (list to restore)
	state   string // originating list's encoded state (card origin only)
	step    editStep
	find    string        // captured "old" term once step >= editAwaitPick
	pickIdx int           // chosen occurrence for Replace: -1 = all (default); >=0 = that 0-based match
	menuMsg tele.Editable // the note message to edit in place
}

func (tb *Bot) currentEditState() *pendingEdit {
	tb.editMu.Lock()
	defer tb.editMu.Unlock()
	return tb.editState
}

func (tb *Bot) setEditState(pe *pendingEdit) {
	tb.editMu.Lock()
	tb.editState = pe
	tb.editMu.Unlock()
}

func (tb *Bot) clearEditState() {
	tb.editMu.Lock()
	tb.editState = nil
	tb.editMu.Unlock()
}

// editOriginFromData decodes an edit button's payload: a bare note id means a
// saved-reply origin (kind ""); an id:kind:state triple means a card origin
// (so cancel / finish can rebuild the exact list view).
func editOriginFromData(data string) (id int64, kind, state string, ok bool) {
	if ref, refOK := parseCardRef(data); refOK {
		return ref.id, ref.kind, ref.state, true
	}
	n, err := strconv.ParseInt(strings.TrimSpace(data), 10, 64)
	if err != nil || n <= 0 {
		return 0, "", "", false
	}
	return n, "", "", true
}

// editOriginData re-encodes an origin into a button payload — the inverse of
// editOriginFromData, so every menu/prompt button carries the same origin.
func editOriginData(id int64, kind, state string) string {
	if kind == "" {
		return strconv.FormatInt(id, 10)
	}
	return cardRef{id: id, kind: kind, state: state}.encode()
}

// cancelKB is the lone [✗ Cancel] keyboard shown under a typed-answer prompt.
func (tb *Bot) cancelKB(data string) *tele.ReplyMarkup {
	cancel := editCancelBtn
	cancel.Text = tb.msg.DeleteNoBtn
	cancel.Data = data
	return &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{{cancel}}}
}

// renderEditMenu builds the edit-menu body (header + note preview) and its
// [🔤][📝] / [✗] keyboard. A non-empty header overrides the default question —
// used to prepend a "word not found" note when a replace misses.
func (tb *Bot) renderEditMenu(ctx context.Context, id int64, kind, state, header string) (string, *tele.ReplyMarkup, error) {
	n, err := tb.db.GetNote(ctx, id)
	if err != nil {
		return "", nil, err
	}
	body := header
	if body == "" {
		body = tb.msg.EditPrompt(id)
	}
	if preview := previewText(strings.ReplaceAll(n.RawText, "\n", " "), savedPreviewLen); preview != "" {
		body += "\n\n«" + preview + "»"
	}
	data := editOriginData(id, kind, state)
	repl := editReplaceBtn
	repl.Text = tb.msg.EditReplaceBtn
	repl.Data = data
	full := editFullBtn
	full.Text = tb.msg.EditFullBtn
	full.Data = data
	cancel := editCancelBtn
	cancel.Text = tb.msg.DeleteNoBtn
	cancel.Data = data
	kb := &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{{repl, full}, {cancel}}}
	return body, kb, nil
}

// renderEditedNote rebuilds the originating view once an edit lands or is
// cancelled: the full card for a note opened from a list, or the saved-reply
// (📝 #id «text») for a standalone note. updated=true swaps the saved-reply's
// 📝 line for the "✏️ updated" confirmation so the change is visible.
func (tb *Bot) renderEditedNote(ctx context.Context, id int64, kind, state string, updated bool) (string, *tele.ReplyMarkup, error) {
	if kind != "" {
		return tb.renderCard(ctx, cardRef{id: id, kind: kind, state: state})
	}
	n, err := tb.db.GetNote(ctx, id)
	if err != nil {
		return "", nil, err
	}
	flat := strings.ReplaceAll(n.RawText, "\n", " ")
	truncated := len([]rune(flat)) > savedPreviewLen
	preview := previewText(flat, savedPreviewLen)
	body := "📝 #" + strconv.FormatInt(id, 10)
	switch {
	case updated:
		body = tb.msg.EditUpdated(id, preview)
	case preview != "":
		body += "\n\n«" + preview + "»"
	}
	return body, tb.savedMarkup(id, truncated), nil
}

// cbEditOpen handles [✏️ Edit]: it turns the tapped message into the edit menu
// in place, so no extra message is created.
func (tb *Bot) cbEditOpen(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	id, kind, state, ok := editOriginFromData(cb.Data)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	tb.clearEditState() // a fresh ✏️ supersedes any half-finished edit
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body, kb, err := tb.renderEditMenu(ctx, id, kind, state, "")
	if err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			tb.tryEdit(c, tb.msg.NotFound(id))
			return c.Respond()
		}
		return tb.errToast(c, "refresh", err)
	}
	tb.tryEdit(c, body, kb)
	return c.Respond()
}

// cbEditReplace handles [🔤 Replace a word] — arm the flow to capture the word
// to find next.
func (tb *Bot) cbEditReplace(c tele.Context) error { return tb.cbEditAskText(c, editAwaitFind) }

// cbEditFull handles [📝 Rewrite all] — arm the flow to capture the whole new
// text next.
func (tb *Bot) cbEditFull(c tele.Context) error { return tb.cbEditAskText(c, editAwaitFull) }

// cbEditAskText edits the menu into a typed-answer prompt (with the note
// preview for context) and arms editState so the next typed message is routed
// to continueEdit.
func (tb *Bot) cbEditAskText(c tele.Context, step editStep) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	id, kind, state, ok := editOriginFromData(cb.Data)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	n, err := tb.db.GetNote(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			tb.clearEditState()
			tb.tryEdit(c, tb.msg.NotFound(id))
			return c.Respond()
		}
		return tb.errToast(c, "refresh", err)
	}
	head := tb.msg.EditAskFind(id)
	if step == editAwaitFull {
		head = tb.msg.EditAskFull(id)
	}
	body := head
	if preview := previewText(strings.ReplaceAll(n.RawText, "\n", " "), savedPreviewLen); preview != "" {
		body += "\n\n«" + preview + "»"
	}
	tb.setEditState(&pendingEdit{noteID: id, kind: kind, state: state, step: step, menuMsg: c.Message()})
	tb.tryEdit(c, body, tb.cancelKB(editOriginData(id, kind, state)))
	return c.Respond()
}

// cbEditCancel handles [✗ Cancel] anywhere in the flow: clear the state and
// restore the note message in place. Nothing was changed.
func (tb *Bot) cbEditCancel(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	id, kind, state, ok := editOriginFromData(cb.Data)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	tb.clearEditState()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body, kb, err := tb.renderEditedNote(ctx, id, kind, state, false)
	if err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			tb.tryEdit(c, tb.msg.NotFound(id))
			return c.Respond()
		}
		return tb.errToast(c, "refresh", err)
	}
	tb.tryEdit(c, body, kb)
	return c.Respond()
}

// continueEdit consumes a typed message as the next step of a button-driven
// edit: it applies the change (or advances the replace flow), updates the note
// message in place, and deletes the user's typed message. The edit state is
// cleared once the flow is done.
func (tb *Bot) continueEdit(msg *tele.Message, pe *pendingEdit) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, kb, done, err := tb.advanceEdit(ctx, pe, msg.Text)
	if err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			tb.clearEditState()
			tb.tryEditMsg(pe.menuMsg, tb.msg.NotFound(pe.noteID))
			tb.deleteMsg(msg)
			return nil
		}
		// Transient DB error: leave the edit armed so the user can retry.
		tb.logger.Warn("continueEdit", "err", err)
		return nil
	}
	tb.tryEditMsg(pe.menuMsg, body, kb)
	tb.deleteMsg(msg)
	if done {
		tb.clearEditState()
	}
	return nil
}

// advanceEdit applies one typed step and returns what the note message should
// now show, whether the flow is finished, and any DB error. It mutates pe when
// moving from "which word" to "replace with what".
func (tb *Bot) advanceEdit(ctx context.Context, pe *pendingEdit, text string) (string, *tele.ReplyMarkup, bool, error) {
	text = strings.TrimSpace(text)
	switch pe.step {
	case editAwaitFull:
		if text == "" { // empty → treat as cancel
			body, kb, err := tb.renderEditedNote(ctx, pe.noteID, pe.kind, pe.state, false)
			return body, kb, true, err
		}
		if err := tb.archiveText(ctx, pe.noteID, text); err != nil {
			return "", nil, false, err
		}
		body, kb, err := tb.renderEditedNote(ctx, pe.noteID, pe.kind, pe.state, true)
		return body, kb, true, err

	case editAwaitFind:
		if text == "" {
			body, kb, err := tb.renderEditedNote(ctx, pe.noteID, pe.kind, pe.state, false)
			return body, kb, true, err
		}
		n, err := tb.db.GetNote(ctx, pe.noteID)
		if err != nil {
			return "", nil, false, err
		}
		cnt := strings.Count(n.RawText, text)
		if cnt == 0 {
			// Not there — back to the menu with a short note; user retries or cancels.
			body, kb, err := tb.renderEditMenu(ctx, pe.noteID, pe.kind, pe.state, tb.msg.EditNotFound(text))
			return body, kb, true, err
		}
		pe.find = text
		if cnt == 1 {
			// Unambiguous — go straight to the replacement (pickIdx -1 = the one).
			pe.pickIdx = -1
			pe.step = editAwaitRepl
			return tb.msg.EditAskReplace(text, 1), tb.cancelKB(editOriginData(pe.noteID, pe.kind, pe.state)), false, nil
		}
		// Ambiguous: let the user point at ONE occurrence (or all) before typing
		// the replacement, instead of silently rewriting every hit.
		pe.step = editAwaitPick
		body, kb, err := tb.renderEditPick(ctx, pe)
		return body, kb, false, err

	case editAwaitPick:
		// The user is expected to TAP an occurrence here; a typed message is a
		// mis-tap — re-show the picker rather than swallow the text as an edit.
		body, kb, err := tb.renderEditPick(ctx, pe)
		return body, kb, false, err

	case editAwaitRepl:
		n, err := tb.db.GetNote(ctx, pe.noteID)
		if err != nil {
			return "", nil, false, err
		}
		newText, ok := tb.applyReplace(n.RawText, pe, text)
		if !ok {
			// The find term vanished between steps (rare; e.g. an MCP-side edit)
			// — nothing to replace, back to the menu.
			body, kb, err := tb.renderEditMenu(ctx, pe.noteID, pe.kind, pe.state, tb.msg.EditNotFound(pe.find))
			return body, kb, true, err
		}
		if err := tb.archiveText(ctx, pe.noteID, newText); err != nil {
			return "", nil, false, err
		}
		body, kb, err := tb.renderEditedNote(ctx, pe.noteID, pe.kind, pe.state, true)
		return body, kb, true, err
	}
	// Unknown step — restore and finish.
	body, kb, err := tb.renderEditedNote(ctx, pe.noteID, pe.kind, pe.state, false)
	return body, kb, true, err
}

// --- Replace-occurrence picker -------------------------------------------

// editPickData encodes a picker button payload: the chosen occurrence index
// (-1 = all) followed by the edit origin (bare id or id:kind:state). find and
// the replacement live in the in-memory editState, so only the index needs to
// travel through the 64-byte callback budget.
func editPickData(idx int, id int64, kind, state string) string {
	return strconv.Itoa(idx) + ":" + editOriginData(id, kind, state)
}

// editPickFromData is the inverse of editPickData.
func editPickFromData(data string) (idx int, id int64, kind, state string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(data), ":", 2)
	if len(parts) != 2 {
		return 0, 0, "", "", false
	}
	i, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, "", "", false
	}
	id, kind, state, ok = editOriginFromData(parts[1])
	return i, id, kind, state, ok
}

// applyReplace computes the new note text for the replace flow: every occurrence
// when pe.pickIdx < 0, otherwise just the chosen 0-based occurrence. ok is false
// when the find term is no longer present (or the chosen occurrence is gone).
func (tb *Bot) applyReplace(raw string, pe *pendingEdit, repl string) (string, bool) {
	if pe.pickIdx < 0 {
		if !strings.Contains(raw, pe.find) {
			return "", false
		}
		return strings.ReplaceAll(raw, pe.find, repl), true
	}
	return replaceNth(raw, pe.find, repl, pe.pickIdx)
}

// replaceNth replaces the nth (0-based) non-overlapping occurrence of old in s
// with neu, matching strings.ReplaceAll's left-to-right, non-overlapping order.
// ok is false when s has fewer than n+1 occurrences.
func replaceNth(s, old, neu string, n int) (string, bool) {
	if old == "" || n < 0 {
		return s, false
	}
	off := 0
	for i := 0; ; i++ {
		j := strings.Index(s[off:], old)
		if j < 0 {
			return s, false
		}
		pos := off + j
		if i == n {
			return s[:pos] + neu + s[pos+len(old):], true
		}
		off = pos + len(old)
	}
}

// occurrencePositions returns the byte offsets of up to `limit` non-overlapping
// occurrences of old in s (same order as replaceNth / strings.ReplaceAll).
func occurrencePositions(s, old string, limit int) []int {
	if old == "" || limit <= 0 {
		return nil
	}
	var pos []int
	off := 0
	for len(pos) < limit {
		j := strings.Index(s[off:], old)
		if j < 0 {
			break
		}
		pos = append(pos, off+j)
		off += j + len(old)
	}
	return pos
}

// snippetAround returns a short, rune-safe context window around the match at
// byte offset pos, with the match itself wrapped in ‹ › so overlapping contexts
// stay tellable apart. Ellipses mark truncation on either side.
func snippetAround(s string, pos, matchLen, window int) string {
	lo := pos - window
	if lo < 0 {
		lo = 0
	}
	hi := pos + matchLen + window
	if hi > len(s) {
		hi = len(s)
	}
	seg := s[lo:pos] + "‹" + s[pos:pos+matchLen] + "›" + s[pos+matchLen:hi]
	seg = strings.ToValidUTF8(seg, "") // drop any partial runes the byte window clipped
	pre, suf := "", ""
	if lo > 0 {
		pre = "…"
	}
	if hi < len(s) {
		suf = "…"
	}
	return pre + seg + suf
}

// editPickMax caps how many individual occurrences the picker lists; beyond it
// the user can still Replace all (or refine the find term).
const editPickMax = 6

// editPickWindow is the bytes of context shown on each side of a match in the
// picker (~12 Cyrillic chars).
const editPickWindow = 24

// renderEditPick builds the occurrence picker: a numbered, context-quoted list
// of matches with one [n] button each, plus [Replace all] and [✗ Cancel].
func (tb *Bot) renderEditPick(ctx context.Context, pe *pendingEdit) (string, *tele.ReplyMarkup, error) {
	n, err := tb.db.GetNote(ctx, pe.noteID)
	if err != nil {
		return "", nil, err
	}
	pos := occurrencePositions(n.RawText, pe.find, editPickMax+1)
	more := len(pos) > editPickMax
	if more {
		pos = pos[:editPickMax]
	}
	var body strings.Builder
	body.WriteString(tb.msg.EditPickHeader(pe.find, len(pos), more))
	var rows [][]tele.InlineButton
	var numRow []tele.InlineButton
	for i, p := range pos {
		body.WriteString(fmt.Sprintf("\n%d. %s", i+1, snippetAround(n.RawText, p, len(pe.find), editPickWindow)))
		btn := editPickBtn
		btn.Text = strconv.Itoa(i + 1)
		btn.Data = editPickData(i, pe.noteID, pe.kind, pe.state)
		numRow = append(numRow, btn)
		if len(numRow) == 3 {
			rows = append(rows, numRow)
			numRow = nil
		}
	}
	if len(numRow) > 0 {
		rows = append(rows, numRow)
	}
	all := editPickBtn
	all.Text = tb.msg.EditPickAllBtn
	all.Data = editPickData(-1, pe.noteID, pe.kind, pe.state)
	cancel := editCancelBtn
	cancel.Text = tb.msg.DeleteNoBtn
	cancel.Data = editOriginData(pe.noteID, pe.kind, pe.state)
	rows = append(rows, []tele.InlineButton{all}, []tele.InlineButton{cancel})
	return body.String(), &tele.ReplyMarkup{InlineKeyboard: rows}, nil
}

// cbEditPick handles a picker tap: it records the chosen occurrence (-1 = all)
// on the in-memory editState and asks for the replacement text in place.
func (tb *Bot) cbEditPick(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	idx, id, kind, state, ok := editPickFromData(cb.Data)
	if !ok {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	pe := tb.currentEditState()
	if pe == nil || pe.noteID != id || pe.find == "" {
		// editState was lost (e.g. a restart) — the picker buttons are stale.
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.EditExpired})
	}
	pe.pickIdx = idx
	pe.step = editAwaitRepl
	tb.setEditState(pe)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	count := 1 // a specific occurrence → singular prompt
	if idx < 0 {
		if note, gerr := tb.db.GetNote(ctx, id); gerr == nil {
			count = strings.Count(note.RawText, pe.find)
		}
	}
	tb.tryEdit(c, tb.msg.EditAskReplace(pe.find, count), tb.cancelKB(editOriginData(id, kind, state)))
	return c.Respond()
}

// archiveText archives the note's current text to notes_history and replaces
// it with newText in one transaction.
func (tb *Bot) archiveText(ctx context.Context, id int64, newText string) error {
	_, err := tb.db.ArchiveAndUpdateText(ctx, id, newText, "", db.NoteMeta{})
	return err
}

// tryEditMsg edits a message we hold a reference to (not the one under the
// current context) — used from onText, where c points at the user's typed
// message rather than the note. Benign "not modified" is ignored. No-op when
// the bot isn't wired (unit tests).
func (tb *Bot) tryEditMsg(msg tele.Editable, what interface{}, opts ...interface{}) {
	if tb.bot == nil || msg == nil {
		return
	}
	if _, err := tb.bot.Edit(msg, what, opts...); err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return
		}
		tb.logger.Warn("bot.Edit failed", "err", err)
	}
}

// deleteMsg removes the user's typed edit input best-effort so the chat stays
// clean. In a private chat a bot may delete incoming messages; failures (age,
// permissions) are non-fatal. No-op when the bot isn't wired (unit tests).
func (tb *Bot) deleteMsg(msg *tele.Message) {
	if tb.bot == nil || msg == nil {
		return
	}
	if err := tb.bot.Delete(msg); err != nil {
		tb.logger.Debug("delete user edit input", "err", err)
	}
}
