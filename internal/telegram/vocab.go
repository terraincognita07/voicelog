package telegram

import (
	"context"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"voicelog/internal/db"
)

// /vocab UI — interactive vocabulary editor.
//
// Inline view:
//   [term1 ❌] [term2 ❌]
//   [term3 ❌]
//   [➕ Add] [🗑 Clear]
//
// Text fallback (preserved for batch / scripting):
//   /vocab list
//   /vocab add <term> [<term>...]
//   /vocab del <term>
//   /vocab clear confirm
//
// Add flow uses Telegram's ForceReply: a separate prompt message that
// the user replies to. onText() detects the reply via ReplyTo.Sender.ID
// + exact prompt text match.

// MaxVocabTermLen caps the length of a single vocabulary term. The
// whisper prompt buffer is finite (~224 tokens silently truncated); one
// pathological 10k-char "term" would dominate it and degrade
// transcription quality for everything else. Enforced in cmdVocab
// add-paths; the DB-level AddVocab also trims whitespace.
const MaxVocabTermLen = 64

var (
	vocabRmBtn       = tele.InlineButton{Unique: "vocab_rm"}       // remove one term
	vocabAddBtn      = tele.InlineButton{Unique: "vocab_add"}      // open add prompt
	vocabClearAskBtn = tele.InlineButton{Unique: "vocab_clr_ask"}  // show confirm
	vocabClearYesBtn = tele.InlineButton{Unique: "vocab_clr_yes"}  // confirm wipe
	vocabClearNoBtn  = tele.InlineButton{Unique: "vocab_clr_no"}   // cancel wipe
)

// addVocabTerms walks the slice, applies MaxVocabTermLen rule, calls
// db.AddVocab for each non-skipped term. Returns (added, skipped) where
// skipped counts terms rejected for length.
func (tb *Bot) addVocabTerms(ctx context.Context, terms []string) (added, skipped int, err error) {
	for _, term := range terms {
		t := strings.TrimSpace(term)
		if t == "" {
			continue
		}
		if len([]rune(t)) > MaxVocabTermLen {
			skipped++
			continue
		}
		ok, e := tb.db.AddVocab(ctx, t)
		if e != nil {
			return added, skipped, e
		}
		if ok {
			added++
		}
	}
	return added, skipped, nil
}

func (tb *Bot) cmdVocab(c tele.Context) error {
	args := strings.Fields(c.Message().Payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if len(args) == 0 || args[0] == "list" {
		body, kb, err := tb.renderVocab(ctx)
		if err != nil {
			return tb.errReply(c, "vocab list", err)
		}
		if kb == nil {
			return c.Send(body)
		}
		return c.Send(body, kb)
	}

	switch args[0] {
	case "add":
		if len(args) < 2 {
			return c.Send(tb.msg.VocabUsage)
		}
		added, skipped, err := tb.addVocabTerms(ctx, args[1:])
		if err != nil {
			return tb.errReply(c, "vocab add", err)
		}
		return tb.sendVocabWithPrefix(c, ctx, tb.msg.VocabAdded(added, len(args)-1)+tb.msg.VocabSkippedSuffix(skipped))
	case "del", "rm", "remove":
		if len(args) != 2 {
			return c.Send(tb.msg.VocabUsage)
		}
		ok, err := tb.db.RemoveVocab(ctx, args[1])
		if err != nil {
			return tb.errReply(c, "vocab del", err)
		}
		return tb.sendVocabWithPrefix(c, ctx, tb.msg.VocabRemoved(args[1], ok))
	case "clear":
		// Two-step: require `clear confirm` to actually wipe.
		if len(args) != 2 || args[1] != "confirm" {
			terms, err := tb.db.ListVocab(ctx)
			if err != nil {
				return tb.errReply(c, "vocab list", err)
			}
			return c.Send(tb.msg.VocabClearAsk(len(terms)) + "\n\n" + tb.msg.VocabClearFallback)
		}
		n, err := tb.db.ClearVocab(ctx)
		if err != nil {
			return tb.errReply(c, "vocab clear", err)
		}
		return c.Send(tb.msg.VocabCleared(n))
	default:
		return c.Send(tb.msg.VocabUsage)
	}
}

// renderVocab returns the body text + inline keyboard for the /vocab view.
// Empty state shows a single [➕ Add] (no Clear when nothing to wipe).
func (tb *Bot) renderVocab(ctx context.Context) (string, *tele.ReplyMarkup, error) {
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	terms, err := tb.db.ListVocab(dbCtx)
	if err != nil {
		return "", nil, err
	}
	body := tb.msg.VocabHeader(len(terms))
	if len(terms) == 0 {
		body = tb.msg.EmptyVocab
	}
	var rows [][]tele.InlineButton
	var current []tele.InlineButton
	for _, t := range terms {
		b := vocabRmBtn
		b.Text = tb.msg.VocabRmBtn(t)
		b.Data = t
		current = append(current, b)
		if len(current) == 2 {
			rows = append(rows, current)
			current = nil
		}
	}
	if len(current) > 0 {
		rows = append(rows, current)
	}
	addB := vocabAddBtn
	addB.Text = tb.msg.VocabAddBtn
	clearB := vocabClearAskBtn
	clearB.Text = tb.msg.VocabClearBtn
	if len(terms) == 0 {
		rows = append(rows, []tele.InlineButton{addB})
	} else {
		rows = append(rows, []tele.InlineButton{addB, clearB})
	}
	return body, &tele.ReplyMarkup{InlineKeyboard: rows}, nil
}

// sendVocabWithPrefix re-renders the vocab view after a mutation and
// sends it as a new message, prefixed with a short confirmation line.
// Used by text-mode /vocab add|del so the user sees the result list
// instead of just "✓ added N".
func (tb *Bot) sendVocabWithPrefix(c tele.Context, ctx context.Context, prefix string) error {
	body, kb, err := tb.renderVocab(ctx)
	if err != nil {
		return tb.errReply(c, "refresh", err)
	}
	if kb == nil {
		return c.Send(prefix + "\n\n" + body)
	}
	return c.Send(prefix+"\n\n"+body, kb)
}

func (tb *Bot) editWithVocab(c tele.Context, body string, kb *tele.ReplyMarkup) {
	if kb == nil {
		tb.tryEdit(c, body)
		return
	}
	tb.tryEdit(c, body, kb)
}

func (tb *Bot) cbVocabRemove(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	term := strings.TrimSpace(cb.Data)
	if term == "" {
		return c.Respond()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tb.db.RemoveVocab(ctx, term); err != nil {
		return tb.errToast(c, "vocab rm", err)
	}
	body, kb, err := tb.renderVocab(ctx)
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithVocab(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbVocabAddPrompt(c tele.Context) error {
	_ = c.Respond()
	return c.Send(tb.msg.VocabAddPrompt, &tele.ReplyMarkup{ForceReply: true, Selective: true})
}

func (tb *Bot) cbVocabClearAsk(c tele.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	terms, err := tb.db.ListVocab(ctx)
	if err != nil {
		return tb.errToast(c, "vocab list", err)
	}
	yes := vocabClearYesBtn
	yes.Text = tb.msg.VocabYesBtn
	no := vocabClearNoBtn
	no.Text = tb.msg.VocabNoBtn
	kb := &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{{yes, no}}}
	tb.tryEdit(c, tb.msg.VocabClearAsk(len(terms)), kb)
	return c.Respond()
}

func (tb *Bot) cbVocabClearYes(c tele.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tb.db.ClearVocab(ctx); err != nil {
		return tb.errToast(c, "vocab clear", err)
	}
	body, kb, err := tb.renderVocab(ctx)
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithVocab(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbVocabClearNo(c tele.Context) error {
	body, kb, err := tb.renderVocab(context.Background())
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithVocab(c, body, kb)
	return c.Respond()
}

// onText handles plain text messages. Today's only use: the /vocab "Add"
// force-reply. When the user replies to the prompt sent by
// cbVocabAddPrompt, we parse the reply as space-separated terms. Reply
// detection: the message's ReplyTo must point to a message authored by
// this bot AND its text must exactly equal VocabAddPrompt.
func (tb *Bot) onText(c tele.Context) error {
	msg := c.Message()
	if msg == nil || msg.ReplyTo == nil {
		return nil
	}
	if msg.ReplyTo.Sender == nil || tb.bot.Me == nil || msg.ReplyTo.Sender.ID != tb.bot.Me.ID {
		return nil
	}
	if strings.TrimSpace(msg.ReplyTo.Text) != tb.msg.VocabAddPrompt {
		return nil
	}
	terms := strings.Fields(msg.Text)
	if len(terms) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	added, skipped, err := tb.addVocabTerms(ctx, terms)
	if err != nil {
		return tb.errReply(c, "vocab add", err)
	}
	body, kb, err := tb.renderVocab(ctx)
	if err != nil {
		return tb.errReply(c, "refresh", err)
	}
	confirmation := tb.msg.VocabAdded(added, len(terms)) + tb.msg.VocabSkippedSuffix(skipped)
	if kb == nil {
		return c.Send(confirmation + "\n\n" + body)
	}
	return c.Send(confirmation+"\n\n"+body, kb)
}

// Ensure db package is referenced (avoids unused import in fast-edit
// scenarios where we temporarily remove the last usage).
var _ = db.MaxVocabTerms
