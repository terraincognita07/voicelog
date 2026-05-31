package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/terraincognita07/voicelog/internal/db"
)

// Saved-reply UX — the message the bot sends after capturing a note. The
// reply ships with an inline keyboard:
//
//	[🗑 Delete] [✏️ Edit]    (+ [📖 Show full] when the preview was clipped)
//
// 🗑 is a permanent, irreversible delete, so it asks first: the message
// swaps to "Delete #N permanently?" with [✓ Yes, delete] [✗ Cancel].
// Cancel restores the saved-reply; Yes erases the note row, its edit
// history (ON DELETE CASCADE) and its retained audio file.

var (
	deleteBtn    = tele.InlineButton{Unique: "delete"}     // 🗑 on a saved-note reply → confirm
	deleteYesBtn = tele.InlineButton{Unique: "delete_yes"} // confirm the delete
	deleteNoBtn  = tele.InlineButton{Unique: "delete_no"}  // cancel the delete
	savedFullBtn = tele.InlineButton{Unique: "saved_full"} // expand preview to full text
	editBtn      = tele.InlineButton{Unique: "edit_note"}  // open the force-reply edit prompt
)

// savedPreviewLen caps the inline preview shown under a saved-reply.
// Longer transcriptions get a [📖 Show full] button that swaps the
// truncated preview for the full text (still within Telegram's 4096-byte
// message limit; whisper output > 3500 chars is truncated by the
// renderer below — a rare edge for typical voice notes).
const savedPreviewLen = 200

// savedMarkup is the inline keyboard for a saved-note reply: the
// [🗑 Delete] [✏️ Edit] row, plus a [📖 Show full] row when the inline
// preview was clipped.
func (tb *Bot) savedMarkup(id int64, truncated bool) *tele.ReplyMarkup {
	idStr := strconv.FormatInt(id, 10)
	del := deleteBtn
	del.Text = tb.msg.DeleteBtn
	del.Data = idStr
	edit := editBtn
	edit.Text = tb.msg.EditBtn
	edit.Data = idStr
	rows := [][]tele.InlineButton{{del, edit}}
	if truncated {
		full := savedFullBtn
		full.Text = tb.msg.ShowFullBtn
		full.Data = idStr
		rows = append(rows, []tele.InlineButton{full})
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

// cbDeleteAsk handles [🗑 Delete] under a saved-note reply. Because the
// delete is irreversible it does not act immediately — it swaps the message
// to a confirm prompt with [✓ Yes, delete] [✗ Cancel].
func (tb *Bot) cbDeleteAsk(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(cb.Data), 10, 64)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	idStr := strconv.FormatInt(id, 10)
	yes := deleteYesBtn
	yes.Text = tb.msg.DeleteYesBtn
	yes.Data = idStr
	no := deleteNoBtn
	no.Text = tb.msg.DeleteNoBtn
	no.Data = idStr
	kb := &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{{yes, no}}}
	tb.tryEdit(c, tb.msg.DeleteAsk(id), kb)
	return c.Respond()
}

// cbDeleteYes performs the confirmed delete. The note row, its edit history
// (ON DELETE CASCADE) and its retained audio file are removed for good.
func (tb *Bot) cbDeleteYes(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(cb.Data), 10, 64)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tb.deleteNote(ctx, id); err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			tb.tryEdit(c, tb.msg.NotFound(id))
			return c.Respond()
		}
		return tb.errToast(c, "delete", err)
	}
	tb.tryEdit(c, tb.msg.Deleted(id))
	return c.Respond()
}

// cbDeleteNo cancels a pending delete and restores the saved-note reply
// (preview + the 🗑/✏️ keyboard). The note was never touched.
func (tb *Bot) cbDeleteNo(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(cb.Data), 10, 64)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	preview, truncated := tb.notePreviewAndTruncated(ctx, id, savedPreviewLen)
	body := "📝 #" + strconv.FormatInt(id, 10)
	if preview != "" {
		body += "\n\n«" + preview + "»"
	}
	tb.tryEdit(c, body, tb.savedMarkup(id, truncated))
	return c.Respond()
}

// cbSavedFull replaces the truncated preview with the full transcription.
// After expanding, the [📖 Show full] button is removed (no point offering
// it again), but the 🗑/✏️ row stays.
func (tb *Bot) cbSavedFull(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(cb.Data), 10, 64)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	n, err := tb.db.GetNote(ctx, id)
	if err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			tb.tryEdit(c, tb.msg.NotFound(id))
			return c.Respond()
		}
		return tb.errToast(c, "refresh", err)
	}
	// Hard-cap the rendered body so we stay safely under Telegram's
	// 4096-byte message limit even with a header line.
	full := previewText(n.RawText, 3500)
	body := "📖 #" + strconv.FormatInt(id, 10) + "\n\n«" + full + "»"
	tb.tryEdit(c, body, tb.savedMarkup(id, false))
	return c.Respond()
}

// cbEditPrompt handles [✏️ Edit] under a saved-note reply. It opens a
// force-reply prompt carrying the note id; the user's reply is caught in
// onText (via matchEditPrompt) and applied through applyNoteEdit. Using a
// force-reply (rather than asking the user to type a command with the id)
// keeps editing one tap + one message, the same shape as /vocab Add.
func (tb *Bot) cbEditPrompt(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(cb.Data), 10, 64)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	_ = c.Respond()
	return c.Send(tb.msg.EditPrompt(id), &tele.ReplyMarkup{ForceReply: true, Selective: true})
}

// matchEditPrompt reports whether promptText is an edit force-reply prompt
// and, if so, the note id it targets. It recovers the id by reading the
// first run of digits in the text and confirming that re-rendering
// EditPrompt(id) reproduces the exact prompt — locale-agnostic, and robust
// as long as EditPrompt embeds the id as its only number (asserted in
// TestLocalesAreComplete).
func (tb *Bot) matchEditPrompt(promptText string) (int64, bool) {
	id, ok := firstInt(promptText)
	if !ok {
		return 0, false
	}
	if tb.msg.EditPrompt(id) != promptText {
		return 0, false
	}
	return id, true
}

// firstInt returns the first maximal run of ASCII digits in s as an int64.
func firstInt(s string) (int64, bool) {
	start := -1
	for i, r := range s {
		if r >= '0' && r <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			n, err := strconv.ParseInt(s[start:i], 10, 64)
			return n, err == nil
		}
	}
	if start >= 0 {
		n, err := strconv.ParseInt(s[start:], 10, 64)
		return n, err == nil
	}
	return 0, false
}

// splitEdit detects a "find → replace" instruction in an edit reply. It
// accepts a space-padded arrow / ASCII separator — so an arrow buried in a
// full-text replacement ("x -> y") doesn't trip it — plus the bare Unicode
// arrow (rare in journal prose). Returns (find, replacement, true) on a hit.
func splitEdit(reply string) (find, repl string, ok bool) {
	for _, sep := range []string{" → ", " -> ", " => ", "→"} {
		if i := strings.Index(reply, sep); i >= 0 {
			return strings.TrimSpace(reply[:i]), strings.TrimSpace(reply[i+len(sep):]), true
		}
	}
	return "", "", false
}

// applyNoteEdit applies an edit reply to a note — two shapes behind one
// ✏️ button:
//   - "old → new"  → replace every occurrence of `old` with `new` in the
//     current text (case-sensitive). Fixes one whisper-misheard word without
//     retyping the whole transcript.
//   - anything else → replace the whole text (the original behavior).
//
// Either way the previous text is archived to notes_history (recoverable at
// the SQL level). An edit that can't apply (the `find` isn't present, or the
// result would empty the note) reports back and changes nothing.
func (tb *Bot) applyNoteEdit(c tele.Context, id int64, reply string) error {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	newText := reply // default: full-text replacement
	if find, repl, isReplace := splitEdit(reply); isReplace {
		if find == "" {
			return c.Send(tb.msg.EditUsage)
		}
		n, err := tb.db.GetNote(ctx, id)
		if err != nil {
			if errors.Is(err, db.ErrNoteNotFound) {
				return c.Send(tb.msg.NotFound(id))
			}
			return tb.errReply(c, "edit note", err)
		}
		if !strings.Contains(n.RawText, find) {
			return c.Send(tb.msg.EditNotFound(find))
		}
		newText = strings.ReplaceAll(n.RawText, find, repl)
	}
	if strings.TrimSpace(newText) == "" {
		return c.Send(tb.msg.EditUsage)
	}

	if _, err := tb.db.ArchiveAndUpdateText(ctx, id, newText, "", db.NoteMeta{}); err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			return c.Send(tb.msg.NotFound(id))
		}
		return tb.errReply(c, "edit note", err)
	}
	preview, truncated := tb.notePreviewAndTruncated(ctx, id, savedPreviewLen)
	return c.Send(tb.msg.EditUpdated(id, preview), tb.savedMarkup(id, truncated))
}

// notePreviewAndTruncated returns the truncated preview AND whether
// truncation happened. Callers use the bool to decide whether to attach
// the [📖 Show full] button.
func (tb *Bot) notePreviewAndTruncated(ctx context.Context, id int64, maxRunes int) (string, bool) {
	n, err := tb.db.GetNote(ctx, id)
	if err != nil {
		return "", false
	}
	flat := strings.ReplaceAll(n.RawText, "\n", " ")
	if len([]rune(flat)) > maxRunes {
		return string([]rune(flat)[:maxRunes]) + "…", true
	}
	return flat, false
}
