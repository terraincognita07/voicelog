package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/terraincognita07/voicelog/internal/audio"
	"github.com/terraincognita07/voicelog/internal/db"
	"github.com/terraincognita07/voicelog/internal/diskguard"
	"github.com/terraincognita07/voicelog/internal/promptbuilder"
	"github.com/terraincognita07/voicelog/internal/whisper"
)

// dedupWindow is how recent a note must be (by created_at) to be
// considered the same audio as a freshly arriving voice message. 5
// minutes is long enough to catch double-taps on slow mobile networks
// while staying short enough that intentional re-sends of the same
// recorded clip later in the day go through normally.
const dedupWindow = 5 * time.Minute

// transcriber is the subset of *whisper.Client that processFile depends on.
// Defined consumer-side so tests can inject a fake without standing up an
// HTTP server or ffmpeg. *whisper.Client satisfies it; cmd/bot keeps passing
// the concrete type — only the field on Bot is widened.
type transcriber interface {
	Transcribe(ctx context.Context, srcPath, prompt string) (whisper.Result, error)
}

type Bot struct {
	bot                 *tele.Bot
	db                  *db.DB
	whisper             transcriber
	allowedUser         int64
	logger              *slog.Logger
	msg                 messages
	basePrompt          string
	audioDir            string  // persistent audio storage; "" = retention disabled
	audioRetainOn       bool    // memoizes audioDir != "" for processFile
	hallucinationThresh float64 // no_speech_prob > thresh → suspect; default 0.6
	dataDir             string  // filesystem to check for free space
	minFreeDiskBytes    uint64  // disk-full threshold; 0 disables the guard

	mainMenu       *tele.ReplyMarkup
	btnMenuPending *tele.Btn
	btnMenuRecent  *tele.Btn
	btnMenuVocab   *tele.Btn
	btnMenuHelp    *tele.Btn

	// rejectLog throttles "rejected from unknown user" warnings so a
	// spammer can't fill the disk with one log line per attempt.
	rejectLog   map[int64]time.Time
	rejectLogMu sync.Mutex

	// editState is the in-flight button-driven note edit, if any. The bot is
	// single-user (gated by allowOnly), so one slot is enough — a second ✏️
	// simply replaces it. nil means no edit is in progress. Guarded by editMu.
	editState *pendingEdit
	editMu    sync.Mutex
}

// rejectLogWindow is the per-user cool-down between rejection warnings.
// Tuned for "one entry per spammer per ~15 min" so a continuous attack
// generates at most ~4 lines/hour instead of one-per-message.
const rejectLogWindow = 15 * time.Minute

// Config bundles the optional knobs that don't fit cleanly into the
// positional New() signature. Zero value = defaults (no audio retention,
// no base whisper prompt, en locale, default hallucination threshold,
// no disk-full guard).
type Config struct {
	Locale              string  // "en" / "ru" / fallback "en"
	BasePrompt          string  // WHISPER_PROMPT env
	AudioDir            string  // "" disables audio retention
	HallucinationThresh float64 // no_speech_prob threshold; 0 → default 0.6
	DataDir             string  // filesystem location of the DB / audio dir; used by the disk-full guard
	MinFreeDiskMB       uint64  // refuse new captures when free space drops below this; 0 disables
}

// defaultHallucinationThresh is whisper.cpp's conventional cutoff for
// "this segment is probably not speech". 0.6 catches obvious silence/
// music while still tolerating quiet voice.
const defaultHallucinationThresh = 0.6

// New creates a Bot. locale is "en" or "ru" (or any other key in the
// locales map). An empty or unknown locale falls back to "en".
// basePrompt is the admin-default whisper prompt (env WHISPER_PROMPT) —
// vocabulary terms are appended at transcribe time. cfg.AudioDir, when
// non-empty, enables on-disk retention of the original .oga files (see
// internal/audio).
func New(token string, cfg Config, allowedUser int64, store *db.DB, w transcriber, logger *slog.Logger) (*Bot, error) {
	b, err := tele.NewBot(tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, fmt.Errorf("init telebot: %w", err)
	}
	thresh := cfg.HallucinationThresh
	if thresh <= 0 {
		thresh = defaultHallucinationThresh
	}
	tb := &Bot{
		bot:                 b,
		db:                  store,
		whisper:             w,
		allowedUser:         allowedUser,
		logger:              logger,
		msg:                 pickLocale(cfg.Locale),
		basePrompt:          strings.TrimSpace(cfg.BasePrompt),
		audioDir:            strings.TrimSpace(cfg.AudioDir),
		audioRetainOn:       strings.TrimSpace(cfg.AudioDir) != "",
		hallucinationThresh: thresh,
		dataDir:             strings.TrimSpace(cfg.DataDir),
		minFreeDiskBytes:    cfg.MinFreeDiskMB * 1024 * 1024,
		rejectLog:           make(map[int64]time.Time),
	}
	tb.buildMenu()
	tb.registerHandlers()
	return tb, nil
}

// buildMenu wires the persistent reply-keyboard (bottom of screen).
// ReplyButton instances are stored on the Bot so we can route taps to
// the same handlers as the equivalent / commands.
func (tb *Bot) buildMenu() {
	m := &tele.ReplyMarkup{ResizeKeyboard: true}
	pending := m.Text(tb.msg.MenuPending)
	recent := m.Text(tb.msg.MenuRecent)
	vocab := m.Text(tb.msg.MenuVocab)
	help := m.Text(tb.msg.MenuHelp)
	m.Reply(
		m.Row(pending, recent),
		m.Row(vocab, help),
	)
	tb.mainMenu = m
	tb.btnMenuPending = &pending
	tb.btnMenuRecent = &recent
	tb.btnMenuVocab = &vocab
	tb.btnMenuHelp = &help
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
	tb.bot.Handle("/start", tb.cmdStart)
	tb.bot.Handle("/help", tb.cmdHelp)
	tb.bot.Handle("/vocab", tb.cmdVocab)
	tb.bot.Handle(&deleteBtn, tb.cbDeleteAsk)
	tb.bot.Handle(&deleteYesBtn, tb.cbDeleteYes)
	tb.bot.Handle(&deleteNoBtn, tb.cbDeleteNo)
	tb.bot.Handle(&savedFullBtn, tb.cbSavedFull)
	tb.bot.Handle(&editBtn, tb.cbEditOpen)
	tb.bot.Handle(&editReplaceBtn, tb.cbEditReplace)
	tb.bot.Handle(&editFullBtn, tb.cbEditFull)
	tb.bot.Handle(&editCancelBtn, tb.cbEditCancel)
	tb.bot.Handle(&openCardBtn, tb.cbOpenCard)
	tb.bot.Handle(&cardTagsBtn, tb.cbCardTags)
	tb.bot.Handle(&cardTagsBackBtn, tb.cbCardTagsBack)
	tb.bot.Handle(&cardTagAddBtn, tb.cbCardTagAdd)
	tb.bot.Handle(&cardTagRemoveBtn, tb.cbCardTagRemove)
	tb.bot.Handle(&cardDeleteBtn, tb.cbCardDeleteAsk)
	tb.bot.Handle(&cardDeleteYesBtn, tb.cbCardDeleteYes)
	tb.bot.Handle(&cardDeleteNoBtn, tb.cbCardDeleteNo)
	tb.bot.Handle(&cardBackBtn, tb.cbCardBack)
	tb.bot.Handle(&vocabRmBtn, tb.cbVocabRemove)
	tb.bot.Handle(&vocabAddBtn, tb.cbVocabAddPrompt)
	tb.bot.Handle(&vocabClearAskBtn, tb.cbVocabClearAsk)
	tb.bot.Handle(&vocabClearYesBtn, tb.cbVocabClearYes)
	tb.bot.Handle(&vocabClearNoBtn, tb.cbVocabClearNo)
	tb.bot.Handle(&pendingMoreBtn, tb.cbPendingMore)
	tb.bot.Handle(&recentMoreBtn, tb.cbRecentMore)
	tb.bot.Handle(&recentFilterBtn, tb.cbRecentFilter)
	tb.bot.Handle(&pendingClearAskBtn, tb.cbPendingClearAsk)
	tb.bot.Handle(&pendingClearYesBtn, tb.cbPendingClearYes)
	tb.bot.Handle(&pendingClearNoBtn, tb.cbPendingClearNo)
	tb.bot.Handle(&pendingDayBtn, tb.cbPendingDay)
	tb.bot.Handle(&recentDayBtn, tb.cbRecentDay)
	tb.bot.Handle(tele.OnText, tb.onText)

	tb.bot.Handle(tb.btnMenuPending, tb.cmdPending)
	tb.bot.Handle(tb.btnMenuRecent, tb.cmdRecent)
	tb.bot.Handle(tb.btnMenuVocab, tb.cmdVocab)
	tb.bot.Handle(tb.btnMenuHelp, tb.cmdHelp)

	tb.syncMenu()
}

// syncMenu pushes the locale-specific command hints into Telegram's /-menu
// (the blue button next to the input). Errors are logged but never block
// startup — bot must come up even if the API rejects the SetCommands call.
func (tb *Bot) syncMenu() {
	cmds := make([]tele.Command, 0, len(tb.msg.Commands))
	for _, h := range tb.msg.Commands {
		cmds = append(cmds, tele.Command{Text: h.Cmd, Description: h.Desc})
	}
	if err := tb.bot.SetCommands(cmds); err != nil {
		tb.logger.Warn("setCommands", "err", err)
		return
	}
	tb.logger.Info("setCommands ok", "count", len(cmds))
}

func (tb *Bot) allowOnly() tele.MiddlewareFunc {
	return func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			sender := c.Sender()
			if sender == nil || sender.ID != tb.allowedUser {
				if sender != nil {
					tb.logRejectionThrottled(sender.ID, sender.Username)
				}
				return nil // silent drop
			}
			return next(c)
		}
	}
}

// logRejectionThrottled emits at most one Warn per user-id per
// rejectLogWindow so a spammer can't bloat the log. Map is unbounded by
// distinct user IDs in theory, but each entry is ~32 bytes — millions of
// distinct spammers would still fit in single-digit MB.
func (tb *Bot) logRejectionThrottled(fromID int64, username string) {
	tb.rejectLogMu.Lock()
	last, seen := tb.rejectLog[fromID]
	now := time.Now()
	if seen && now.Sub(last) < rejectLogWindow {
		tb.rejectLogMu.Unlock()
		return
	}
	tb.rejectLog[fromID] = now
	tb.rejectLogMu.Unlock()
	tb.logger.Warn("rejected message from unknown user",
		"from_id", fromID, "username", username)
}

func (tb *Bot) cmdStart(c tele.Context) error {
	return c.Send(tb.msg.Welcome, tb.mainMenu)
}

func (tb *Bot) cmdHelp(c tele.Context) error {
	return c.Send(tb.msg.Help, tb.mainMenu)
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

	// Disk-full guard: bail out BEFORE downloading 200 KB and burning a
	// whisper pass on something that won't fit. Skipped when minFreeDiskBytes
	// is 0 (env unset) or dataDir is empty.
	if tb.minFreeDiskBytes > 0 && tb.dataDir != "" {
		if free, err := diskguard.FreeBytes(tb.dataDir); err != nil {
			tb.logger.Warn("disk guard: statfs", "dir", tb.dataDir, "err", err)
		} else if free < tb.minFreeDiskBytes {
			tb.logger.Warn("disk almost full — capture refused",
				"free_mb", free/1024/1024,
				"min_mb", tb.minFreeDiskBytes/1024/1024,
			)
			return c.Send(tb.msg.DiskFull(free/1024/1024, tb.minFreeDiskBytes/1024/1024))
		}
	}

	// Send "typing" so the user sees the bot is alive while whisper runs.
	// Telebot refreshes the indicator every ~5s; one shot is enough for
	// short clips, and a fresh shot will be sent if we send a new message.
	_ = c.Notify(tele.Typing)

	tmpDir, err := os.MkdirTemp("", "voicelog-*")
	if err != nil {
		return tb.errReply(c, "tmp dir", err)
	}
	defer os.RemoveAll(tmpDir)

	srcPath := filepath.Join(tmpDir, "src")
	if err := tb.bot.Download(file, srcPath); err != nil {
		return tb.errReply(c, "download from telegram", err)
	}

	return tb.processSource(ctx, c, srcPath, duration)
}

// processSource is everything after we have an on-disk audio file: dedup
// lookup, whisper transcribe, DB insert, audio retention, saved-reply. Split
// from processFile so unit tests can feed a pre-prepared temp file without
// running ffmpeg or hitting telebot. Production caller (processFile) supplies
// the timeout-bounded ctx; tests can pass any context.
func (tb *Bot) processSource(ctx context.Context, c tele.Context, srcPath string, duration int) error {
	// Dedup: SHA-256 the downloaded bytes BEFORE we burn ffmpeg+whisper
	// cycles on what may be a double-tap. A miss here just costs an
	// open+read+hash; a hit saves the whole pipeline.
	audioHash, err := hashFile(srcPath)
	if err != nil {
		// Hashing should be infallible at this point (file exists, we
		// just wrote it). Log and continue — dedup is a nice-to-have,
		// not a gate on saving.
		tb.logger.Warn("dedup hash", "err", err)
	} else if audioHash != "" {
		dbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		dup, derr := tb.db.FindRecentByHash(dbCtx, audioHash, dedupWindow)
		cancel()
		if derr == nil {
			tb.logger.Info("dedup hit", "existing_id", dup.ID, "age_sec", int(time.Since(dup.CreatedAt).Seconds()))
			return c.Send(tb.msg.Duplicate(dup.ID, int(time.Since(dup.CreatedAt).Seconds())))
		}
		// Any error other than ErrNoteNotFound is treated as "no dup":
		// transient DB hiccup must not block the user's recording.
		if !errors.Is(derr, db.ErrNoteNotFound) {
			tb.logger.Warn("dedup lookup", "err", derr)
		}
	}

	prompt := promptbuilder.Compose(ctx, tb.db, tb.basePrompt, tb.logger)
	tb.logger.Info("transcribing", "path", srcPath, "duration_sec", duration, "prompt_len", len(prompt))
	_ = c.Notify(tele.Typing) // refresh the indicator before the long call
	result, err := tb.whisper.Transcribe(ctx, srcPath, prompt)
	if err != nil {
		return tb.errReply(c, "whisper", err)
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		return c.Send(tb.msg.EmptyTrans)
	}

	// Derive quality signals. Missing segments → store NULL.
	meta := db.NoteMeta{
		AudioHash:      audioHash,
		DedupWindowSec: int64(dedupWindow.Seconds()),
	}
	overall, worst, suspect, ok := result.Aggregate(tb.hallucinationThresh)
	if ok {
		meta.ConfidenceOverall = &overall
		meta.ConfidenceMin = &worst
		meta.SuspectHallucination = suspect
	}
	id, err := tb.db.InsertNoteWithMeta(ctx, text, duration, meta)
	if err != nil {
		if errors.Is(err, db.ErrDuplicateAudio) {
			// Race: another goroutine inserted the same audio between our
			// FindRecentByHash check and this insert. InsertNoteWithMeta's
			// conditional INSERT ... WHERE NOT EXISTS catches it (no UNIQUE
			// constraint involved); we surface the same Duplicate reply the
			// fast-lane would have. id is the surviving row.
			dup, derr := tb.db.FindRecentByHash(ctx, audioHash, dedupWindow)
			if derr == nil {
				return c.Send(tb.msg.Duplicate(dup.ID, int(time.Since(dup.CreatedAt).Seconds())))
			}
			// Lookup failed (shouldn't happen — we just confirmed the row
			// exists) — fall back to a zero-age message so the user still
			// gets feedback instead of an "insert note" error.
			return c.Send(tb.msg.Duplicate(id, 0))
		}
		return tb.errReply(c, "insert note", err)
	}

	// Opt-in audio retention. Failures here MUST NOT block the saved-
	// reply — the transcription is the user-visible product; audio is a
	// backup. Log and continue.
	if tb.audioRetainOn {
		if savedPath, sErr := audio.SaveOriginal(srcPath, tb.audioDir, id); sErr != nil {
			tb.logger.Warn("audio retain: save failed", "id", id, "err", sErr)
		} else if pErr := tb.db.SetAudioPath(ctx, id, savedPath); pErr != nil {
			tb.logger.Warn("audio retain: set path failed", "id", id, "err", pErr)
			// Path-set failed but file exists — orphan it; janitor will
			// not pick it up. Caller-side risk for the operator.
		}
	}

	pending, _ := tb.db.CountPending(ctx)
	preview := previewText(text, savedPreviewLen)
	truncated := len([]rune(strings.ReplaceAll(text, "\n", " "))) > savedPreviewLen
	return c.Send(tb.msg.Recorded(id, duration, pending, preview, meta.SuspectHallucination), tb.savedMarkup(id, truncated))
}

// hashFile streams the file at path through SHA-256 and returns the hex
// digest. Used for dedup of double-tap voice messages: identical bytes
// → identical hash → looked up in the recent-notes window before
// transcription.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// previewText returns a single-line, run-length-capped preview suitable
// for the saved-reply / delete confirmation. Newlines are flattened to
// spaces; runs over `cap` are truncated with an ellipsis.
func previewText(s string, cap int) string {
	flat := strings.ReplaceAll(s, "\n", " ")
	runes := []rune(flat)
	if len(runes) > cap {
		return string(runes[:cap]) + "…"
	}
	return flat
}

// deleteNote permanently removes a note and its retained audio file. The
// note's edit history is cleared by the ON DELETE CASCADE FK; the audio
// removal is best-effort (a stuck file never blocks the delete).
func (tb *Bot) deleteNote(ctx context.Context, id int64) error {
	audioPath, err := tb.db.DeleteNote(ctx, id)
	if err != nil {
		return err
	}
	audio.Delete(tb.audioDir, audioPath, tb.logger)
	return nil
}

// deleteAllPending permanently removes every pending note and reclaims the
// retained audio of each. Returns the number of notes removed.
func (tb *Bot) deleteAllPending(ctx context.Context) (int, error) {
	paths, n, err := tb.db.DeleteAllPending(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range paths {
		audio.Delete(tb.audioDir, p, tb.logger)
	}
	return n, nil
}

// cmdDelete is the typed /delete <id> power-user path. Unlike the inline 🗑
// button it deletes immediately without a confirm step — typing the id is
// itself the explicit, deliberate action.
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
	if err := tb.deleteNote(ctx, id); err != nil {
		if errors.Is(err, db.ErrNoteNotFound) {
			return c.Send(tb.msg.NotFound(id))
		}
		return tb.errReply(c, "delete", err)
	}
	return c.Send(tb.msg.Deleted(id))
}
