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
	bot         *tele.Bot
	db          *db.DB
	whisper     *whisper.Client
	allowedUser int64
	logger      *slog.Logger
}

func New(token string, allowedUser int64, store *db.DB, w *whisper.Client, logger *slog.Logger) (*Bot, error) {
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
	}
	tb.registerHandlers()
	return tb, nil
}

func (tb *Bot) Start() { tb.bot.Start() }
func (tb *Bot) Stop()  { tb.bot.Stop() }

func (tb *Bot) registerHandlers() {
	tb.bot.Use(tb.allowOnly())

	tb.bot.Handle(tele.OnVoice, tb.onVoice)
	tb.bot.Handle(tele.OnAudio, tb.onAudio)
	tb.bot.Handle("/pending", tb.cmdPending)
	tb.bot.Handle("/recent", tb.cmdRecent)
	tb.bot.Handle("/delete", tb.cmdDelete)
	tb.bot.Handle("/start", tb.cmdHelp)
	tb.bot.Handle("/help", tb.cmdHelp)
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
	return c.Send("voicelog — шли голосовое или аудио.\n\n" +
		"/pending — последние 20 необработанных\n" +
		"/recent — последние 10 (любой статус)\n" +
		"/delete <id> — пометить как discarded")
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

	tb.logger.Info("transcribing", "path", srcPath, "duration_sec", duration)
	text, err := tb.whisper.Transcribe(ctx, srcPath)
	if err != nil {
		return tb.errReply(c, "whisper", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return c.Send("⚠ пустая транскрипция")
	}

	id, err := tb.db.InsertNote(ctx, text, duration)
	if err != nil {
		return tb.errReply(c, "insert note", err)
	}

	pending, _ := tb.db.CountPending(ctx)
	return c.Send(fmt.Sprintf("✓ записано #%d (%d pending)", id, pending))
}

func (tb *Bot) errReply(c tele.Context, label string, err error) error {
	tb.logger.Error(label, "err", err)
	return c.Send(fmt.Sprintf("⚠ %s: %v", label, err))
}

func (tb *Bot) cmdPending(c tele.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	notes, err := tb.db.ListPending(ctx, 20)
	if err != nil {
		return tb.errReply(c, "list pending", err)
	}
	return c.Send(formatNotes(notes, false))
}

func (tb *Bot) cmdRecent(c tele.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	notes, err := tb.db.ListRecent(ctx, 10)
	if err != nil {
		return tb.errReply(c, "list recent", err)
	}
	return c.Send(formatNotes(notes, true))
}

func (tb *Bot) cmdDelete(c tele.Context) error {
	args := strings.Fields(c.Message().Payload)
	if len(args) != 1 {
		return c.Send("использование: /delete <id>")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return c.Send("id должен быть числом")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tb.db.MarkDiscarded(ctx, id); err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			return c.Send(fmt.Sprintf("не найдено #%d (или уже discarded)", id))
		}
		return tb.errReply(c, "mark discarded", err)
	}
	return c.Send(fmt.Sprintf("✓ #%d → discarded", id))
}

func formatNotes(notes []db.Note, withStatus bool) string {
	if len(notes) == 0 {
		return "пусто"
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
