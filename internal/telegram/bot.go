package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"voicelog/internal/db"
	"voicelog/internal/whisper"
)

type Bot struct {
	bot          *tele.Bot
	db           *db.DB
	whisper      *whisper.Client
	allowedUser  int64
	logger       *slog.Logger
	msg          messages
	basePrompt   string
}

// New creates a Bot. locale is "en" or "ru" (or any other key in the
// locales map). An empty or unknown locale falls back to "en".
// basePrompt is the admin-default whisper prompt (env WHISPER_PROMPT) —
// vocabulary terms are appended at transcribe time.
func New(token, locale, basePrompt string, allowedUser int64, store *db.DB, w *whisper.Client, logger *slog.Logger) (*Bot, error) {
	b, err := tele.NewBot(tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("init telebot: %w", err)
	}
	tb := &Bot{
		bot:         b,
		db:          store,
		whisper:     w,
		allowedUser: allowedUser,
		logger:      logger,
		msg:         pickLocale(locale),
		basePrompt:  strings.TrimSpace(basePrompt),
	}
	tb.registerHandlers()
	return tb, nil
}

func (tb *Bot) Start() { tb.bot.Start() }
func (tb *Bot) Stop()  { tb.bot.Stop() }

// discardBtn is the prototype inline button used to route callbacks. Its
// Unique field is the routing key; per-message Data carries the note ID.
var discardBtn = tele.InlineButton{Unique: "discard"}

func (tb *Bot) registerHandlers() {
	tb.bot.Use(tb.allowOnly())

	tb.bot.Handle(tele.OnVoice, tb.onVoice)
	tb.bot.Handle(tele.OnAudio, tb.onAudio)
	tb.bot.Handle("/pending", tb.cmdPending)
	tb.bot.Handle("/recent", tb.cmdRecent)
	tb.bot.Handle("/delete", tb.cmdDelete)
	tb.bot.Handle("/start", tb.cmdHelp)
	tb.bot.Handle("/help", tb.cmdHelp)
	tb.bot.Handle("/vocab", tb.cmdVocab)
	tb.bot.Handle(&discardBtn, tb.cbDiscard)
}

func (tb *Bot) allowOnly() tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			sender := c.Sender()
			if sender == nil || sender.ID != tb.allowedUser {
				if sender != nil {
					tb.logger.Warn("rejected message from unknown user",
						"from_id", sender.ID, "username", sender.Username)
				}
				return nil // silent drop
			}
			return next(c)
		}
	}
}

func (tb *Bot) cmdHelp(c tele.Context) error {
	return c.Send(tb.msg.Help)
}

func (tb *Bot) onVoice(c tele.Context) error {
	v := c.Message().Voice
	return tb.processFile(c, v.MediaFile(), v.Duration)
}

func (tb *Bot) onAudio(c tele.Context) error {
	a := c.Message().Audio
	return tb.processFile(c, a.MediaFile(), a.Duration)
}

func (tb *Bot) processFile(c tele.Context, file *tele.File, duration int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "voicelog-*")
	if err != nil {
		return tb.errReply(c, "tmp dir", err)
	}
	defer os.RemoveAll(tmpDir)

	srcPath := filepath.Join(tmpDir, "src")
	if err := tb.bot.Download(file, srcPath); err != nil {
		return tb.errReply(c, "download from telegram", err)
	}

	prompt := tb.composePrompt(ctx)
	tb.logger.Info("transcribing", "path", srcPath, "duration_sec", duration, "prompt_len", len(prompt))
	text, err := tb.whisper.Transcribe(ctx, srcPath, prompt)
	if err != nil {
		return tb.errReply(c, "whisper", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return c.Send(tb.msg.EmptyTrans)
	}

	id, err := tb.db.InsertNote(ctx, text, duration)
	if err != nil {
		return tb.errReply(c, "insert note", err)
	}

	pending, _ := tb.db.CountPending(ctx)
	return c.Send(tb.msg.Recorded(id, pending), tb.discardMarkup(id))
}

// discardMarkup builds a one-button inline keyboard tied to a specific note.
func (tb *Bot) discardMarkup(id int64) *tele.ReplyMarkup {
	btn := discardBtn
	btn.Text = tb.msg.DiscardBtn
	btn.Data = strconv.FormatInt(id, 10)
	return &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{{btn}}}
}

// cbDiscard handles the inline [🗑 Discard] press attached to a saved-note
// reply. Telegram callback data is the bare note ID.
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
		tb.logger.Error("callback discard", "id", id, "err", err)
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("discard", err)})
	}
	_ = c.Edit(tb.msg.DiscardedReply(id))
	return c.Respond()
}

func (tb *Bot) errReply(c tele.Context, label string, err error) error {
	tb.logger.Error(label, "err", err)
	return c.Send(tb.msg.Error(label, err))
}

func (tb *Bot) cmdPending(c tele.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	notes, err := tb.db.ListPending(ctx, 20)
	if err != nil {
		return tb.errReply(c, "list pending", err)
	}
	return c.Send(tb.formatNotes(notes, false))
}

func (tb *Bot) cmdRecent(c tele.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	notes, err := tb.db.ListRecent(ctx, 10)
	if err != nil {
		return tb.errReply(c, "list recent", err)
	}
	return c.Send(tb.formatNotes(notes, true))
}

func (tb *Bot) cmdDelete(c tele.Context) error {
	args := strings.Fields(c.Message().Payload)
	if len(args) != 1 {
		return c.Send(tb.msg.UsageDelete)
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return c.Send(tb.msg.BadID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tb.db.MarkDiscarded(ctx, id); err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			return c.Send(tb.msg.NotFound(id))
		}
		return tb.errReply(c, "mark discarded", err)
	}
	return c.Send(tb.msg.Discarded(id))
}

// composePrompt builds the whisper "initial prompt" as
//   basePrompt + " " + vocabulary terms
// Falls back gracefully on DB error — base prompt alone is better than
// failing the transcription.
func (tb *Bot) composePrompt(ctx context.Context) string {
	dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	vocab, err := tb.db.VocabPrompt(dbCtx)
	if err != nil {
		tb.logger.Warn("vocab prompt", "err", err)
		return tb.basePrompt
	}
	switch {
	case tb.basePrompt == "" && vocab == "":
		return ""
	case tb.basePrompt == "":
		return vocab
	case vocab == "":
		return tb.basePrompt
	default:
		return tb.basePrompt + " " + vocab
	}
}

func (tb *Bot) cmdVocab(c tele.Context) error {
	args := strings.Fields(c.Message().Payload)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if len(args) == 0 || args[0] == "list" {
		terms, err := tb.db.ListVocab(ctx)
		if err != nil {
			return tb.errReply(c, "vocab list", err)
		}
		return c.Send(tb.msg.VocabList(terms))
	}

	switch args[0] {
	case "add":
		if len(args) < 2 {
			return c.Send(tb.msg.VocabUsage)
		}
		added := 0
		for _, term := range args[1:] {
			ok, err := tb.db.AddVocab(ctx, term)
			if err != nil {
				return tb.errReply(c, "vocab add", err)
			}
			if ok {
				added++
			}
		}
		return c.Send(tb.msg.VocabAdded(added, len(args)-1))
	case "del", "rm", "remove":
		if len(args) != 2 {
			return c.Send(tb.msg.VocabUsage)
		}
		ok, err := tb.db.RemoveVocab(ctx, args[1])
		if err != nil {
			return tb.errReply(c, "vocab del", err)
		}
		return c.Send(tb.msg.VocabRemoved(args[1], ok))
	case "clear":
		// Two-step: require `clear confirm` to actually wipe.
		if len(args) != 2 || args[1] != "confirm" {
			return c.Send(tb.msg.VocabClearAsk)
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

func (tb *Bot) formatNotes(notes []db.Note, withStatus bool) string {
	if len(notes) == 0 {
		return tb.msg.EmptyList
	}
	var b strings.Builder
	for _, n := range notes {
		text := strings.ReplaceAll(n.RawText, "\n", " ")
		runes := []rune(text)
		if len(runes) > 80 {
			text = string(runes[:80]) + "…"
		}
		ts := n.CreatedAt.Format("01-02 15:04")
		if withStatus {
			fmt.Fprintf(&b, "#%d [%s %s] %s\n", n.ID, ts, n.Status, text)
		} else {
			fmt.Fprintf(&b, "#%d [%s] %s\n", n.ID, ts, text)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
