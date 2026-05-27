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
	msg         messages
	basePrompt  string

	mainMenu       *tele.ReplyMarkup
	btnMenuPending *tele.Btn
	btnMenuRecent  *tele.Btn
	btnMenuVocab   *tele.Btn
	btnMenuHelp    *tele.Btn
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

// Callback button prototypes. Each Unique routes to a dedicated handler;
// per-message Data carries the note ID.
var (
	discardBtn        = tele.InlineButton{Unique: "discard"}         // saved-note reply
	discardPendingBtn = tele.InlineButton{Unique: "discard_pending"} // /pending list
	discardRecentBtn  = tele.InlineButton{Unique: "discard_recent"}  // /recent list
	restoreRecentBtn  = tele.InlineButton{Unique: "restore_recent"}  // /recent list
	vocabRmBtn        = tele.InlineButton{Unique: "vocab_rm"}        // remove one term
	vocabAddBtn       = tele.InlineButton{Unique: "vocab_add"}       // open add prompt
	vocabClearAskBtn  = tele.InlineButton{Unique: "vocab_clr_ask"}   // show confirm
	vocabClearYesBtn  = tele.InlineButton{Unique: "vocab_clr_yes"}   // confirm wipe
	vocabClearNoBtn   = tele.InlineButton{Unique: "vocab_clr_no"}    // cancel wipe

	pendingMoreBtn  = tele.InlineButton{Unique: "pending_more"}  // grow /pending list
	recentMoreBtn   = tele.InlineButton{Unique: "recent_more"}   // grow /recent list
	recentFilterBtn = tele.InlineButton{Unique: "recent_filter"} // status filter chip
)

// pendingPageSize / recentPageSize are the default visible windows. They
// grow in pageSize increments on each "Show more" tap, capped at
// maxListNotes to stay under Telegram's 4096-byte message limit.
const (
	pendingPageSize = 20
	recentPageSize  = 10
	maxListNotes    = 40
)

// validRecentFilter returns the canonical status string for the given
// filter chip data. Empty = "all". Unknown = "all" (defensive).
func validRecentFilter(s string) string {
	switch s {
	case "pending", "discarded":
		return s
	default:
		return ""
	}
}

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
	tb.bot.Handle(&discardPendingBtn, tb.cbDiscardPending)
	tb.bot.Handle(&discardRecentBtn, tb.cbDiscardRecent)
	tb.bot.Handle(&restoreRecentBtn, tb.cbRestoreRecent)
	tb.bot.Handle(&vocabRmBtn, tb.cbVocabRemove)
	tb.bot.Handle(&vocabAddBtn, tb.cbVocabAddPrompt)
	tb.bot.Handle(&vocabClearAskBtn, tb.cbVocabClearAsk)
	tb.bot.Handle(&vocabClearYesBtn, tb.cbVocabClearYes)
	tb.bot.Handle(&vocabClearNoBtn, tb.cbVocabClearNo)
	tb.bot.Handle(&pendingMoreBtn, tb.cbPendingMore)
	tb.bot.Handle(&recentMoreBtn, tb.cbRecentMore)
	tb.bot.Handle(&recentFilterBtn, tb.cbRecentFilter)
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
	}
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
	body, kb, err := tb.renderPending(context.Background(), pendingPageSize)
	if err != nil {
		return tb.errReply(c, "list pending", err)
	}
	return tb.sendList(c, body, kb)
}

func (tb *Bot) cmdRecent(c tele.Context) error {
	body, kb, err := tb.renderRecent(context.Background(), "", recentPageSize)
	if err != nil {
		return tb.errReply(c, "list recent", err)
	}
	return tb.sendList(c, body, kb)
}

func (tb *Bot) sendList(c tele.Context, body string, kb *tele.ReplyMarkup) error {
	if kb == nil {
		return c.Send(body)
	}
	return c.Send(body, kb)
}

// clampPage caps requested limit at maxListNotes so the rendered message
// stays under Telegram's 4096-byte limit.
func clampPage(n int) int {
	if n < 1 {
		return 1
	}
	if n > maxListNotes {
		return maxListNotes
	}
	return n
}

// renderPending returns the body text + inline keyboard for /pending.
// Each visible note gets a [🗑 #id] button. If more pending notes exist
// beyond `limit`, a [Show more] row is appended.
func (tb *Bot) renderPending(ctx context.Context, limit int) (string, *tele.ReplyMarkup, error) {
	limit = clampPage(limit)
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Fetch one extra to detect "has more".
	notes, err := tb.db.ListPending(dbCtx, limit+1)
	if err != nil {
		return "", nil, err
	}
	hasMore := len(notes) > limit
	if hasMore {
		notes = notes[:limit]
	}
	if len(notes) == 0 {
		return tb.msg.EmptyList, nil, nil
	}
	body := tb.formatNotes(notes, false)
	var actions []tele.InlineButton
	for _, n := range notes {
		b := discardPendingBtn
		b.Text = "🗑 #" + strconv.FormatInt(n.ID, 10)
		b.Data = strconv.FormatInt(n.ID, 10)
		actions = append(actions, b)
	}
	rows := chunkButtons(actions, 4)
	if hasMore && limit < maxListNotes {
		more := pendingMoreBtn
		more.Text = tb.msg.ShowMoreBtn
		more.Data = strconv.Itoa(limit + pendingPageSize)
		rows = append(rows, []tele.InlineButton{more})
	}
	return body, &tele.ReplyMarkup{InlineKeyboard: rows}, nil
}

// renderRecent returns the body text + inline keyboard for /recent. Top
// row is the status filter chip group ([All]/[Pending]/[Discarded]); the
// active chip is prefixed with FilterActiveMark. Each note gets a 🗑 or
// ↩ button depending on its current status. [Show more] appended when
// more rows exist beyond `limit` for the active filter.
func (tb *Bot) renderRecent(ctx context.Context, filter string, limit int) (string, *tele.ReplyMarkup, error) {
	filter = validRecentFilter(filter)
	limit = clampPage(limit)
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	notes, err := tb.db.ListRecentByStatus(dbCtx, filter, limit+1)
	if err != nil {
		return "", nil, err
	}
	hasMore := len(notes) > limit
	if hasMore {
		notes = notes[:limit]
	}
	rows := [][]tele.InlineButton{tb.recentFilterRow(filter)}
	if len(notes) == 0 {
		return tb.msg.EmptyList, &tele.ReplyMarkup{InlineKeyboard: rows}, nil
	}
	body := tb.formatNotes(notes, true)
	var actions []tele.InlineButton
	for _, n := range notes {
		var b tele.InlineButton
		if n.Status == db.StatusDiscarded {
			b = restoreRecentBtn
			b.Text = "↩ #" + strconv.FormatInt(n.ID, 10)
		} else {
			b = discardRecentBtn
			b.Text = "🗑 #" + strconv.FormatInt(n.ID, 10)
		}
		b.Data = strconv.FormatInt(n.ID, 10)
		actions = append(actions, b)
	}
	rows = append(rows, chunkButtons(actions, 4)...)
	if hasMore && limit < maxListNotes {
		more := recentMoreBtn
		more.Text = tb.msg.ShowMoreBtn
		more.Data = filter + ":" + strconv.Itoa(limit+recentPageSize)
		rows = append(rows, []tele.InlineButton{more})
	}
	return body, &tele.ReplyMarkup{InlineKeyboard: rows}, nil
}

// recentFilterRow returns the [All][Pending][Discarded] chip row with the
// active filter visually marked.
func (tb *Bot) recentFilterRow(active string) []tele.InlineButton {
	mk := func(filter, label string) tele.InlineButton {
		b := recentFilterBtn
		if filter == active {
			b.Text = tb.msg.FilterActiveMark + label
		} else {
			b.Text = label
		}
		b.Data = filter // empty for "all"
		if filter == "" {
			b.Data = "all"
		}
		return b
	}
	return []tele.InlineButton{
		mk("", tb.msg.FilterAllBtn),
		mk("pending", tb.msg.FilterPendingBtn),
		mk("discarded", tb.msg.FilterDiscardedBtn),
	}
}

func (tb *Bot) cbPendingMore(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(cb.Data))
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	body, kb, err := tb.renderPending(context.Background(), limit)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("refresh", err)})
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

// parseRecentData decodes "filter:limit" callback payload. Returns
// (filter, limit). Defaults to ("", recentPageSize) on parse errors.
func parseRecentData(s string) (string, int) {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	filter := ""
	if len(parts) > 0 && parts[0] != "all" {
		filter = validRecentFilter(parts[0])
	}
	limit := recentPageSize
	if len(parts) == 2 {
		if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
			limit = n
		}
	}
	return filter, limit
}

func (tb *Bot) cbRecentMore(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	filter, limit := parseRecentData(cb.Data)
	body, kb, err := tb.renderRecent(context.Background(), filter, limit)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("refresh", err)})
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbRecentFilter(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	raw := strings.TrimSpace(cb.Data)
	filter := ""
	if raw != "all" {
		filter = validRecentFilter(raw)
	}
	body, kb, err := tb.renderRecent(context.Background(), filter, recentPageSize)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("refresh", err)})
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

// chunkButtons packs a flat button list into rows of width n. Telegram's
// rendering looks best at 3–4 per row for narrow phone screens.
func chunkButtons(btns []tele.InlineButton, n int) [][]tele.InlineButton {
	if n < 1 {
		n = 1
	}
	var out [][]tele.InlineButton
	for i := 0; i < len(btns); i += n {
		end := i + n
		if end > len(btns) {
			end = len(btns)
		}
		out = append(out, btns[i:end])
	}
	return out
}

// editWithList re-renders a list view and edits the source message in
// place. Used by list-context callbacks (discard/restore) so the keyboard
// reflects current state after a tap.
func (tb *Bot) editWithList(c tele.Context, body string, kb *tele.ReplyMarkup) {
	if kb == nil {
		_ = c.Edit(body)
		return
	}
	_ = c.Edit(body, kb)
}

func (tb *Bot) cbDiscardPending(c tele.Context) error {
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
	if err := tb.db.MarkDiscarded(ctx, id); err != nil && !errors.Is(err, db.ErrNoteNotFound) {
		tb.logger.Error("cb discard pending", "id", id, "err", err)
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("discard", err)})
	}
	body, kb, err := tb.renderPending(ctx, pendingPageSize)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("refresh", err)})
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbDiscardRecent(c tele.Context) error {
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
	if err := tb.db.MarkDiscarded(ctx, id); err != nil && !errors.Is(err, db.ErrNoteNotFound) {
		tb.logger.Error("cb discard recent", "id", id, "err", err)
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("discard", err)})
	}
	body, kb, err := tb.renderRecent(ctx, "", recentPageSize)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("refresh", err)})
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbRestoreRecent(c tele.Context) error {
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
	if _, err := tb.db.RestoreNote(ctx, id); err != nil && !errors.Is(err, db.ErrNoteNotFound) {
		tb.logger.Error("cb restore recent", "id", id, "err", err)
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("restore", err)})
	}
	body, kb, err := tb.renderRecent(ctx, "", recentPageSize)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("refresh", err)})
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
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

// renderVocab returns the body text + inline keyboard for the /vocab view:
// each term gets a [term ❌] button; bottom row has [➕ Add] and [🗑 Clear].
// Returns nil keyboard if the vocabulary is empty AND only the Add/Clear
// row would be useful — we always include the action row so the user can
// add the first term without typing.
func (tb *Bot) renderVocab(ctx context.Context) (string, *tele.ReplyMarkup, error) {
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	terms, err := tb.db.ListVocab(dbCtx)
	if err != nil {
		return "", nil, err
	}
	body := tb.msg.VocabHeader(len(terms))
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

func (tb *Bot) editWithVocab(c tele.Context, body string, kb *tele.ReplyMarkup) {
	if kb == nil {
		_ = c.Edit(body)
		return
	}
	_ = c.Edit(body, kb)
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
		tb.logger.Error("cb vocab rm", "term", term, "err", err)
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("vocab rm", err)})
	}
	body, kb, err := tb.renderVocab(ctx)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("refresh", err)})
	}
	tb.editWithVocab(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbVocabAddPrompt(c tele.Context) error {
	_ = c.Respond()
	return c.Send(tb.msg.VocabAddPrompt, &tele.ReplyMarkup{ForceReply: true, Selective: true})
}

func (tb *Bot) cbVocabClearAsk(c tele.Context) error {
	yes := vocabClearYesBtn
	yes.Text = tb.msg.VocabYesBtn
	no := vocabClearNoBtn
	no.Text = tb.msg.VocabNoBtn
	kb := &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{{yes, no}}}
	_ = c.Edit(tb.msg.VocabClearAsk, kb)
	return c.Respond()
}

func (tb *Bot) cbVocabClearYes(c tele.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := tb.db.ClearVocab(ctx); err != nil {
		tb.logger.Error("cb vocab clear", "err", err)
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("vocab clear", err)})
	}
	body, kb, err := tb.renderVocab(ctx)
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("refresh", err)})
	}
	tb.editWithVocab(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbVocabClearNo(c tele.Context) error {
	body, kb, err := tb.renderVocab(context.Background())
	if err != nil {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.Error("refresh", err)})
	}
	tb.editWithVocab(c, body, kb)
	return c.Respond()
}

// onText catches plain text messages. The only flow that needs it today is
// the /vocab "Add" force-reply: when the user replies to the prompt sent by
// cbVocabAddPrompt, we parse the reply as space-separated terms.
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
	added := 0
	for _, term := range terms {
		ok, err := tb.db.AddVocab(ctx, term)
		if err != nil {
			return tb.errReply(c, "vocab add", err)
		}
		if ok {
			added++
		}
	}
	body, kb, err := tb.renderVocab(ctx)
	if err != nil {
		return tb.errReply(c, "refresh", err)
	}
	confirmation := tb.msg.VocabAdded(added, len(terms))
	if kb == nil {
		return c.Send(confirmation + "\n\n" + body)
	}
	return c.Send(confirmation+"\n\n"+body, kb)
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
