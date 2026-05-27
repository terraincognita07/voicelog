package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"voicelog/internal/db"
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
	rows := [][]tele.InlineButton{{primary}}
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
			_ = c.Edit(tb.msg.NotFound(id))
			return c.Respond()
		}
		return tb.errToast(c, "discard", err)
	}
	preview, truncated := tb.notePreviewAndTruncated(ctx, id, savedPreviewLen)
	_ = c.Edit(tb.msg.DiscardedReply(id, preview), tb.savedMarkup(id, true, truncated))
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
			_ = c.Edit(tb.msg.NotFound(id))
			return c.Respond()
		}
		return tb.errToast(c, "restore", err)
	}
	preview, truncated := tb.notePreviewAndTruncated(ctx, id, savedPreviewLen)
	_ = c.Edit(tb.msg.RestoredReply(id, preview), tb.savedMarkup(id, false, truncated))
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
			_ = c.Edit(tb.msg.NotFound(id))
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
	_ = c.Edit(body, tb.savedMarkup(id, discarded, false))
	return c.Respond()
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
