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

// Saved-reply UX — the message bot sends after transcribing a voice note.
// The reply ships with a one-button inline keyboard that toggles in place:
//   pending state:   [🗑 Discard]   → on tap: discard + show preview + swap to [↩ Restore]
//   discarded state: [↩ Restore]   → on tap: restore + show preview + swap to [🗑 Discard]
//
// One message, two-way, no spam. The callback data is the bare note ID
// (numeric, ≤20 bytes — well under Telegram's 64-byte limit).

var (
	discardBtn      = tele.InlineButton{Unique: "discard"}       // saved-note reply
	savedRestoreBtn = tele.InlineButton{Unique: "saved_restore"} // undo discard on saved-note reply
	savedFullBtn    = tele.InlineButton{Unique: "saved_full"}    // expand preview to full text
	editBtn         = tele.InlineButton{Unique: "edit_note"}     // open the force-reply edit prompt
)

// savedPreviewLen caps the inline preview shown under a saved-reply.
// Longer transcriptions get a [📖 Show full] button that swaps the
// truncated preview for the full text (still within Telegram's 4096-byte
// message limit; whisper output > 3500 chars is truncated by the
// renderer below — a rare edge for typical voice notes).
const savedPreviewLen = 200

// savedMarkup is the inline keyboard for a saved-note reply. The primary
// row is one toggle button (🗑 Discard / ↩ Restore). If the transcription
// was truncated for preview, a second row offers [📖 Show full].
func (tb *Bot) savedMarkup(id int64, discarded, truncated bool) *tele.ReplyMarkup {
	var primary tele.InlineButton
	if discarded {
		primary = savedRestoreBtn
		primary.Text = tb.msg.RestoreBtn
	} else {
		primary = discardBtn
		primary.Text = tb.msg.DiscardBtn
	}
	primary.Data = strconv.FormatInt(id, 10)
	// The first row pairs the discard toggle with [✏️ Edit]. Editing is
	// offered only while the note is live — a discarded note is the user's
	// "forget this" signal, so we don't invite overwriting it (mirrors the
	// MCP retranscribe refusal). Restoring first re-enables the edit button.
	firstRow := []tele.InlineButton{primary}
	if !discarded {
		edit := editBtn
		edit.Text = tb.msg.EditBtn
		edit.Data = strconv.FormatInt(id, 10)
		firstRow = append(firstRow, edit)
	}
	rows := [][]tele.InlineButton{firstRow}
	if truncated {
		full := savedFullBtn
		full.Text = tb.msg.ShowFullBtn
		// Data encodes "id:discardedFlag" so the full-view callback can
		// rebuild the markup with the correct primary button afterward.
		flag := "0"
		if discarded {
			flag = "1"
		}
		full.Data = strconv.FormatInt(id, 10) + ":" + flag
		rows = append(rows, []tele.InlineButton{full})
	}
	return &tele.ReplyMarkup{InlineKeyboard: rows}
}

// discardMarkup is used by processFile to attach the initial saved-reply
// keyboard. truncated is set when the inline preview was clipped.
func (tb *Bot) discardMarkup(id int64, truncated bool) *tele.ReplyMarkup {
	return tb.savedMarkup(id, false, truncated)
}

// cbDiscard handles [🗑 Discard] under a saved-note reply. Edits the
// message in place: confirmation + preview of the discarded text, and
// the button becomes [↩ Restore] for one-tap undo.
func (tb *Bot) cbDiscard(c tele.Context) error {
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
	if err := tb.db.MarkDiscarded(ctx, id); err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			tb.tryEdit(c, tb.msg.NotFound(id))
			return c.Respond()
		}
		return tb.errToast(c, "discard", err)
	}
	preview, truncated := tb.notePreviewAndTruncated(ctx, id, savedPreviewLen)
	tb.tryEdit(c, tb.msg.DiscardedReply(id, preview), tb.savedMarkup(id, true, truncated))
	return c.Respond()
}

// cbSavedRestore is the inverse of cbDiscard, attached to the [↩ Restore]
// button rendered after a discard.
func (tb *Bot) cbSavedRestore(c tele.Context) error {
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
	if _, err := tb.db.RestoreNote(ctx, id); err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			tb.tryEdit(c, tb.msg.NotFound(id))
			return c.Respond()
		}
		return tb.errToast(c, "restore", err)
	}
	preview, truncated := tb.notePreviewAndTruncated(ctx, id, savedPreviewLen)
	tb.tryEdit(c, tb.msg.RestoredReply(id, preview), tb.savedMarkup(id, false, truncated))
	return c.Respond()
}

// cbSavedFull replaces the truncated preview with the full transcription.
// Callback data: "id:discardedFlag". After expanding the [📖 Show full]
// button is removed (no point offering "show full" again), but the
// primary 🗑/↩ toggle remains.
func (tb *Bot) cbSavedFull(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	parts := strings.SplitN(strings.TrimSpace(cb.Data), ":", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	discarded := len(parts) == 2 && parts[1] == "1"
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
	var body string
	if discarded {
		body = tb.msg.DiscardedReply(id, full)
	} else {
		// In the not-yet-discarded case the header is "✓ saved" — we
		// reconstruct a compact version (no need to re-show duration /
		// pending count; they're stale anyway).
		body = "📖 #" + strconv.FormatInt(id, 10) + "\n\n«" + full + "»"
	}
	tb.tryEdit(c, body, tb.savedMarkup(id, discarded, false))
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

// applyNoteEdit replaces a note's text with newText, archiving the previous
// text to notes_history (reversible at the SQL level). Empty newText is a
// no-op (returns ErrNoteNotFound's sibling: nil reply, handled by caller).
// Returns the inline preview + whether it was truncated so the caller can
// rebuild the saved-reply markup.
func (tb *Bot) applyNoteEdit(c tele.Context, id int64, newText string) error {
	text := strings.TrimSpace(newText)
	if text == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tb.db.ArchiveAndUpdateText(ctx, id, text, "", db.NoteMeta{}); err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			return c.Send(tb.msg.NotFound(id))
		}
		return tb.errReply(c, "edit note", err)
	}
	preview, truncated := tb.notePreviewAndTruncated(ctx, id, savedPreviewLen)
	return c.Send(tb.msg.EditUpdated(id, preview), tb.savedMarkup(id, false, truncated))
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
