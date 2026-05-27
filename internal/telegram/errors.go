package telegram

import (
	tele "gopkg.in/telebot.v3"
)

// Error-surfacing helpers.
//
// Internal failures must NEVER leak raw err.Error() to the chat — that
// previously exposed internal hostnames (whisper), filesystem paths
// (/data/voicelog.db), and third-party HTTP body content. Every callsite
// goes through userErrMsg, which:
//   1. logs the raw err at slog.Error (preserves full context for the operator)
//   2. returns a sanitized, locale-specific user message from messages.Errors
//   3. falls back to messages.ErrFallback if the label is unknown
//
// errReply sends as a new message; errToast surfaces as a callback popup.

// userErrMsg is the single source of truth for converting an internal
// error label to a chat-safe string. Always logs the raw err.
func (tb *Bot) userErrMsg(label string, err error) string {
	tb.logger.Error(label, "err", err)
	if msg, ok := tb.msg.Errors[label]; ok {
		return "⚠ " + msg
	}
	return "⚠ " + tb.msg.ErrFallback
}

// errReply sends the sanitized error as a new Telegram message. Use from
// command handlers (cmdX) where the failing context isn't tied to an
// existing inline-keyboard message.
func (tb *Bot) errReply(c tele.Context, label string, err error) error {
	return c.Send(tb.userErrMsg(label, err))
}

// errToast surfaces the sanitized error as a small popup on a callback
// query (≤200 chars, auto-dismisses). Use from callback handlers (cbX)
// when the underlying message should stay unchanged on failure.
func (tb *Bot) errToast(c tele.Context, label string, err error) error {
	return c.Respond(&tele.CallbackResponse{Text: tb.userErrMsg(label, err)})
}
