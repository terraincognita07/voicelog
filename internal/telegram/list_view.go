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

// /pending and /recent — day-grouped, paginated, filterable inline-keyboard
// views. Every button at render time embeds the current view state in its
// Data field so any callback can mutate state, re-query the DB, and
// re-render in place. Telegram callback data is capped at 64 bytes; the
// encoding (`limit:expDay` for /pending, `filter:limit:expDay` for
// /recent) fits with room to spare even at the worst case.
//
// Defensive parsing throughout: any missing/garbage field falls back to
// safe defaults so stale buttons after a deploy don't crash, and a
// crafted callback can't smuggle arbitrary strings into our state.

// --- buttons --------------------------------------------------------------

var (
	deletePendingBtn    = tele.InlineButton{Unique: "del_pending"}       // 🗑 in /pending → confirm
	deletePendingYesBtn = tele.InlineButton{Unique: "del_pending_y"}     // confirm /pending delete
	deletePendingNoBtn  = tele.InlineButton{Unique: "del_pending_n"}     // cancel /pending delete
	deleteRecentBtn     = tele.InlineButton{Unique: "del_recent"}        // 🗑 in /recent → confirm
	deleteRecentYesBtn  = tele.InlineButton{Unique: "del_recent_y"}      // confirm /recent delete
	deleteRecentNoBtn   = tele.InlineButton{Unique: "del_recent_n"}      // cancel /recent delete
	pendingMoreBtn      = tele.InlineButton{Unique: "pending_more"}      // grow /pending list
	recentMoreBtn       = tele.InlineButton{Unique: "recent_more"}       // grow /recent list
	recentFilterBtn     = tele.InlineButton{Unique: "recent_filter"}     // status filter chip
	pendingClearAskBtn  = tele.InlineButton{Unique: "pending_clear_ask"} // open confirm
	pendingClearYesBtn  = tele.InlineButton{Unique: "pending_clear_yes"} // confirm wipe
	pendingClearNoBtn   = tele.InlineButton{Unique: "pending_clear_no"}  // cancel wipe
	pendingDayBtn       = tele.InlineButton{Unique: "pending_day"}       // toggle day fold in /pending
	recentDayBtn        = tele.InlineButton{Unique: "recent_day"}        // toggle day fold in /recent
)

// pendingPageSize / recentPageSize are the default visible windows. They
// grow in pageSize increments on each "Show more" tap, capped at
// maxListNotes to stay under Telegram's 4096-byte message limit.
const (
	pendingPageSize = 20
	recentPageSize  = 10
	maxListNotes    = 40
)

// --- state encoding -------------------------------------------------------

type pendingState struct {
	Limit  int
	ExpDay string
}

func (s pendingState) encode() string {
	return strconv.Itoa(s.Limit) + ":" + s.ExpDay
}

func (s pendingState) encodeWithID(id int64) string {
	return strconv.FormatInt(id, 10) + ":" + s.encode()
}

func parsePendingState(raw string) pendingState {
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
	s := pendingState{Limit: pendingPageSize}
	if len(parts) > 0 {
		if n, err := strconv.Atoi(parts[0]); err == nil && n > 0 {
			s.Limit = n
		}
	}
	if len(parts) == 2 {
		s.ExpDay = validDateKey(parts[1])
	}
	return s
}

// parsePendingStateWithID consumes one leading int64 then the state encoding.
func parsePendingStateWithID(raw string) (int64, pendingState) {
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	rest := ""
	if len(parts) == 2 {
		rest = parts[1]
	}
	return id, parsePendingState(rest)
}

type recentState struct {
	Filter string
	Limit  int
	ExpDay string
}

func (s recentState) encode() string {
	f := s.Filter
	if f == "" {
		f = "all"
	}
	return f + ":" + strconv.Itoa(s.Limit) + ":" + s.ExpDay
}

func (s recentState) encodeWithID(id int64) string {
	return strconv.FormatInt(id, 10) + ":" + s.encode()
}

func parseRecentState(raw string) recentState {
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 3)
	s := recentState{Limit: recentPageSize}
	if len(parts) > 0 && parts[0] != "all" {
		s.Filter = validRecentFilter(parts[0])
	}
	if len(parts) >= 2 {
		if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
			s.Limit = n
		}
	}
	if len(parts) == 3 {
		s.ExpDay = validDateKey(parts[2])
	}
	return s
}

func parseRecentStateWithID(raw string) (int64, recentState) {
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	rest := ""
	if len(parts) == 2 {
		rest = parts[1]
	}
	return id, parseRecentState(rest)
}

// validRecentFilter returns the canonical status string for the given
// filter chip data. Empty = "all". Unknown = "all" (defensive).
func validRecentFilter(s string) string {
	switch s {
	case "pending":
		return s
	default:
		return ""
	}
}

// validDateKey accepts only "YYYY-MM-DD". Anything else (including empty
// or garbage) collapses to "". Prevents injection of arbitrary strings
// into our state via crafted callback data.
func validDateKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return ""
	}
	return s
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

// --- day grouping ---------------------------------------------------------

type dayGroup struct {
	dateKey string
	label   string
	notes   []db.Note
}

// groupByDay walks notes (already sorted DESC by created_at) and splits
// them into per-day groups. Labels for today / yesterday are localized;
// everything else is rendered via DayLabel (en uses time.Format; ru uses
// a hand-rolled short table).
func (tb *Bot) groupByDay(notes []db.Note) []dayGroup {
	if len(notes) == 0 {
		return nil
	}
	todayKey := time.Now().Format("2006-01-02")
	yesterdayKey := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	var out []dayGroup
	for _, n := range notes {
		key := n.CreatedAt.Format("2006-01-02")
		if len(out) == 0 || out[len(out)-1].dateKey != key {
			var label string
			switch key {
			case todayKey:
				label = tb.msg.DayToday
			case yesterdayKey:
				label = tb.msg.DayYesterday
			default:
				label = tb.msg.DayLabel(n.CreatedAt)
			}
			out = append(out, dayGroup{dateKey: key, label: label})
		}
		out[len(out)-1].notes = append(out[len(out)-1].notes, n)
	}
	return out
}

// formatNoteLine renders one row of a day-grouped list:
//
//	#9 22:04 · text…           (withStatus = false)
//	#9 22:04 [pending] · text… (withStatus = true; status is locale-translated)
func (tb *Bot) formatNoteLine(n db.Note, withStatus bool, tags []string) string {
	text := strings.ReplaceAll(n.RawText, "\n", " ")
	runes := []rune(text)
	if len(runes) > 60 {
		text = string(runes[:60]) + "…"
	}
	ts := n.CreatedAt.Format("15:04")
	var line string
	if withStatus {
		line = fmt.Sprintf("#%d %s [%s] · %s", n.ID, ts, tb.msg.Status(string(n.Status)), text)
	} else {
		line = fmt.Sprintf("#%d %s · %s", n.ID, ts, text)
	}
	if len(tags) > 0 {
		line += "  🏷 " + strings.Join(tags, ", ")
	}
	return line
}

// loadTags best-effort batch-loads tags for the given notes so the list
// renderers can show them. On error it logs and returns nil — tags are a
// display nicety, never a reason to fail the list.
func (tb *Bot) loadTags(ctx context.Context, notes []db.Note) map[int64][]string {
	if len(notes) == 0 {
		return nil
	}
	ids := make([]int64, len(notes))
	for i, n := range notes {
		ids[i] = n.ID
	}
	tags, err := tb.db.TagsForNotes(ctx, ids)
	if err != nil {
		tb.logger.Warn("list: load tags", "err", err)
		return nil
	}
	return tags
}

// renderDayGroupedBody walks the groups and produces the body text. Today
// is always expanded. Other days expanded only when their dateKey matches
// expDay. Collapsed days appear as a header with note count only.
func (tb *Bot) renderDayGroupedBody(groups []dayGroup, expDay string, withStatus bool, tags map[int64][]string) string {
	if len(groups) == 0 {
		return tb.msg.EmptyList
	}
	todayKey := time.Now().Format("2006-01-02")
	var b strings.Builder
	for i, g := range groups {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(tb.msg.DayHeader(g.label, len(g.notes)))
		expanded := g.dateKey == todayKey || g.dateKey == expDay
		if !expanded {
			continue
		}
		for _, n := range g.notes {
			b.WriteByte('\n')
			b.WriteString(tb.formatNoteLine(n, withStatus, tags[n.ID]))
		}
	}
	return b.String()
}

// visibleNotesAndDayToggles splits groups into:
//   - visible notes (today + the expanded day, if any) for which we render
//     action buttons (🗑/↩) right under the body.
//   - day-toggle groups for every non-today day (both collapsed AND the
//     currently expanded one — so the user can collapse the expanded day
//     by tapping its header).
func visibleNotesAndDayToggles(groups []dayGroup, expDay string) ([]db.Note, []dayGroup) {
	todayKey := time.Now().Format("2006-01-02")
	var visible []db.Note
	var toggles []dayGroup
	for _, g := range groups {
		if g.dateKey == todayKey {
			visible = append(visible, g.notes...)
			continue
		}
		if g.dateKey == expDay {
			visible = append(visible, g.notes...)
		}
		toggles = append(toggles, g)
	}
	return visible, toggles
}

// --- helpers --------------------------------------------------------------

func (tb *Bot) sendList(c tele.Context, body string, kb *tele.ReplyMarkup) error {
	if kb == nil {
		return c.Send(body)
	}
	return c.Send(body, kb)
}

// editWithList re-renders a list view and edits the source message in
// place. Used by list-context callbacks (delete confirm) so the keyboard
// reflects current state after a tap.
func (tb *Bot) editWithList(c tele.Context, body string, kb *tele.ReplyMarkup) {
	if kb == nil {
		tb.tryEdit(c, body)
		return
	}
	tb.tryEdit(c, body, kb)
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

// --- /pending -------------------------------------------------------------

func (tb *Bot) cmdPending(c tele.Context) error {
	body, kb, err := tb.renderPending(context.Background(), pendingState{Limit: pendingPageSize})
	if err != nil {
		return tb.errReply(c, "list pending", err)
	}
	return tb.sendList(c, body, kb)
}

// renderPending builds the day-grouped /pending view. State threads
// through every button so taps preserve {limit, expDay}.
func (tb *Bot) renderPending(ctx context.Context, st pendingState) (string, *tele.ReplyMarkup, error) {
	st.Limit = clampPage(st.Limit)
	st.ExpDay = validDateKey(st.ExpDay)

	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	notes, err := tb.db.ListPending(dbCtx, st.Limit+1)
	if err != nil {
		return "", nil, err
	}
	hasMore := len(notes) > st.Limit
	if hasMore {
		notes = notes[:st.Limit]
	}
	if len(notes) == 0 {
		return tb.msg.EmptyPending, nil, nil
	}

	tagMap := tb.loadTags(dbCtx, notes)
	groups := tb.groupByDay(notes)
	body := tb.renderDayGroupedBody(groups, st.ExpDay, false, tagMap)
	visible, toggles := visibleNotesAndDayToggles(groups, st.ExpDay)

	var actions []tele.InlineButton
	for _, n := range visible {
		b := deletePendingBtn
		b.Text = "🗑 #" + strconv.FormatInt(n.ID, 10)
		b.Data = st.encodeWithID(n.ID)
		actions = append(actions, b)
	}
	rows := chunkButtons(actions, 4)

	for _, g := range toggles {
		btn := pendingDayBtn
		btn.Text = tb.msg.DayHeader(g.label, len(g.notes))
		newExp := g.dateKey
		if g.dateKey == st.ExpDay {
			newExp = "" // tapping the expanded day collapses it
		}
		btn.Data = pendingState{Limit: st.Limit, ExpDay: newExp}.encode()
		rows = append(rows, []tele.InlineButton{btn})
	}

	if hasMore && st.Limit < maxListNotes {
		more := pendingMoreBtn
		more.Text = tb.msg.ShowMoreBtn
		more.Data = pendingState{Limit: st.Limit + pendingPageSize, ExpDay: st.ExpDay}.encode()
		rows = append(rows, []tele.InlineButton{more})
	}
	// Clear-all carries the current state so Cancel can return to where
	// the user was.
	clear := pendingClearAskBtn
	clear.Text = tb.msg.ClearAllBtn
	clear.Data = st.encode()
	rows = append(rows, []tele.InlineButton{clear})
	return body, &tele.ReplyMarkup{InlineKeyboard: rows}, nil
}

// --- /recent --------------------------------------------------------------

func (tb *Bot) cmdRecent(c tele.Context) error {
	body, kb, err := tb.renderRecent(context.Background(), recentState{Limit: recentPageSize})
	if err != nil {
		return tb.errReply(c, "list recent", err)
	}
	return tb.sendList(c, body, kb)
}

// renderRecent builds the day-grouped /recent view with filter chips on
// top. withStatus body lines only when filter is "" (all) — otherwise
// status is redundant with the active chip.
func (tb *Bot) renderRecent(ctx context.Context, st recentState) (string, *tele.ReplyMarkup, error) {
	st.Filter = validRecentFilter(st.Filter)
	st.Limit = clampPage(st.Limit)
	st.ExpDay = validDateKey(st.ExpDay)

	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	notes, err := tb.db.ListRecentByStatus(dbCtx, st.Filter, st.Limit+1)
	if err != nil {
		return "", nil, err
	}
	hasMore := len(notes) > st.Limit
	if hasMore {
		notes = notes[:st.Limit]
	}

	rows := [][]tele.InlineButton{tb.recentFilterRow(st.Filter)}
	if len(notes) == 0 {
		return tb.msg.EmptyRecent(st.Filter), &tele.ReplyMarkup{InlineKeyboard: rows}, nil
	}

	tagMap := tb.loadTags(dbCtx, notes)
	groups := tb.groupByDay(notes)
	withStatus := st.Filter == ""
	body := tb.renderDayGroupedBody(groups, st.ExpDay, withStatus, tagMap)
	visible, toggles := visibleNotesAndDayToggles(groups, st.ExpDay)

	var actions []tele.InlineButton
	for _, n := range visible {
		b := deleteRecentBtn
		b.Text = "🗑 #" + strconv.FormatInt(n.ID, 10)
		b.Data = st.encodeWithID(n.ID)
		actions = append(actions, b)
	}
	rows = append(rows, chunkButtons(actions, 4)...)

	for _, g := range toggles {
		btn := recentDayBtn
		btn.Text = tb.msg.DayHeader(g.label, len(g.notes))
		newExp := g.dateKey
		if g.dateKey == st.ExpDay {
			newExp = ""
		}
		btn.Data = recentState{Filter: st.Filter, Limit: st.Limit, ExpDay: newExp}.encode()
		rows = append(rows, []tele.InlineButton{btn})
	}

	if hasMore && st.Limit < maxListNotes {
		more := recentMoreBtn
		more.Text = tb.msg.ShowMoreBtn
		more.Data = recentState{Filter: st.Filter, Limit: st.Limit + recentPageSize, ExpDay: st.ExpDay}.encode()
		rows = append(rows, []tele.InlineButton{more})
	}
	return body, &tele.ReplyMarkup{InlineKeyboard: rows}, nil
}

// recentFilterRow returns the [All][Pending] chip row with the active
// filter visually marked. Chip taps reset the view to a fresh page
// (limit = default, expDay = "").
func (tb *Bot) recentFilterRow(active string) []tele.InlineButton {
	mk := func(filter, label string) tele.InlineButton {
		b := recentFilterBtn
		if filter == active {
			b.Text = tb.msg.FilterActiveMark + label
		} else {
			b.Text = label
		}
		b.Data = filter
		if filter == "" {
			b.Data = "all"
		}
		return b
	}
	return []tele.InlineButton{
		mk("", tb.msg.FilterAllBtn),
		mk("pending", tb.msg.FilterPendingBtn),
	}
}

// --- callbacks ------------------------------------------------------------
//
// Per-note deletion from a list is irreversible, so it runs through a
// three-step confirm: a 🗑 tap (Ask) swaps the body to "Delete #N
// permanently?", then Yes deletes + re-renders the list and No just
// re-renders it. All three steps are generic over the view state type S
// (pendingState / recentState) so /pending and /recent share one
// implementation. Day-toggle and Show-more handlers have no mutation and
// keep their own simpler shape.

// cbListDeleteAsk swaps the list for a "Delete #N permanently?" confirm.
// yesProto/noProto are the view-specific Yes/No buttons; both carry the
// note id + current state so the answer can delete-then-re-render or just
// re-render the exact same view.
func cbListDeleteAsk[S any](
	tb *Bot,
	c tele.Context,
	parseFn func(string) (int64, S),
	encodeWithID func(S, int64) string,
	yesProto, noProto tele.InlineButton,
) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	id, st := parseFn(cb.Data)
	if id == 0 {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	data := encodeWithID(st, id)
	yes := yesProto
	yes.Text = tb.msg.DeleteYesBtn
	yes.Data = data
	no := noProto
	no.Text = tb.msg.DeleteNoBtn
	no.Data = data
	kb := &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{{yes, no}}}
	tb.tryEdit(c, tb.msg.DeleteAsk(id), kb)
	return c.Respond()
}

// cbListDeleteYes deletes the confirmed note (+ its retained audio) and
// re-renders the list in the preserved state.
func cbListDeleteYes[S any](
	tb *Bot,
	c tele.Context,
	parseFn func(string) (int64, S),
	renderFn func(context.Context, S) (string, *tele.ReplyMarkup, error),
) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	id, st := parseFn(cb.Data)
	if id == 0 {
		return c.Respond(&tele.CallbackResponse{Text: tb.msg.BadID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tb.deleteNote(ctx, id); err != nil && !errors.Is(err, db.ErrNoteNotFound) {
		return tb.errToast(c, "delete", err)
	}
	body, kb, err := renderFn(ctx, st)
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

// cbListDeleteNo cancels the delete and re-renders the list unchanged.
func cbListDeleteNo[S any](
	tb *Bot,
	c tele.Context,
	parseFn func(string) (int64, S),
	renderFn func(context.Context, S) (string, *tele.ReplyMarkup, error),
) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	_, st := parseFn(cb.Data)
	body, kb, err := renderFn(context.Background(), st)
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbDeletePendingAsk(c tele.Context) error {
	return cbListDeleteAsk(tb, c, parsePendingStateWithID,
		func(s pendingState, id int64) string { return s.encodeWithID(id) },
		deletePendingYesBtn, deletePendingNoBtn)
}
func (tb *Bot) cbDeletePendingYes(c tele.Context) error {
	return cbListDeleteYes(tb, c, parsePendingStateWithID, tb.renderPending)
}
func (tb *Bot) cbDeletePendingNo(c tele.Context) error {
	return cbListDeleteNo(tb, c, parsePendingStateWithID, tb.renderPending)
}

func (tb *Bot) cbDeleteRecentAsk(c tele.Context) error {
	return cbListDeleteAsk(tb, c, parseRecentStateWithID,
		func(s recentState, id int64) string { return s.encodeWithID(id) },
		deleteRecentYesBtn, deleteRecentNoBtn)
}
func (tb *Bot) cbDeleteRecentYes(c tele.Context) error {
	return cbListDeleteYes(tb, c, parseRecentStateWithID, tb.renderRecent)
}
func (tb *Bot) cbDeleteRecentNo(c tele.Context) error {
	return cbListDeleteNo(tb, c, parseRecentStateWithID, tb.renderRecent)
}

// "View refresh only" callbacks for Show-more / Day-toggle / Filter chip.
// They share the same shape: parse a state, render, edit.

func (tb *Bot) cbPendingMore(c tele.Context) error { return tb.refreshPending(c, parsePendingState) }
func (tb *Bot) cbPendingDay(c tele.Context) error  { return tb.refreshPending(c, parsePendingState) }
func (tb *Bot) cbRecentMore(c tele.Context) error  { return tb.refreshRecent(c, parseRecentState) }
func (tb *Bot) cbRecentDay(c tele.Context) error   { return tb.refreshRecent(c, parseRecentState) }

func (tb *Bot) refreshPending(c tele.Context, parse func(string) pendingState) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	body, kb, err := tb.renderPending(context.Background(), parse(cb.Data))
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

func (tb *Bot) refreshRecent(c tele.Context, parse func(string) recentState) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	body, kb, err := tb.renderRecent(context.Background(), parse(cb.Data))
	if err != nil {
		return tb.errToast(c, "refresh", err)
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
	body, kb, err := tb.renderRecent(context.Background(), recentState{Filter: filter, Limit: recentPageSize})
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}

// --- Clear-all confirm flow ----------------------------------------------
//
// State threads through Ask → Yes/No so Cancel restores the user's
// original view (filter, page, expanded day). Ask button carries
// pendingState in its Data; Yes/No buttons re-encode that state in
// their Data so the renderer hits the same view.

func (tb *Bot) cbPendingClearAsk(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	st := parsePendingState(cb.Data)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	n, err := tb.db.CountPending(ctx)
	if err != nil {
		return tb.errToast(c, "list pending", err)
	}
	encoded := st.encode()
	yes := pendingClearYesBtn
	yes.Text = tb.msg.ClearAllYesBtn
	yes.Data = encoded
	no := pendingClearNoBtn
	no.Text = tb.msg.ClearAllNoBtn
	no.Data = encoded
	kb := &tele.ReplyMarkup{InlineKeyboard: [][]tele.InlineButton{{yes, no}}}
	tb.tryEdit(c, tb.msg.ClearAllAsk(n), kb)
	return c.Respond()
}

func (tb *Bot) cbPendingClearYes(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	st := parsePendingState(cb.Data)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := tb.deleteAllPending(ctx)
	if err != nil {
		return tb.errToast(c, "clear", err)
	}
	tb.logger.Info("pending deleted", "n", n)
	body, kb, rerr := tb.renderPending(ctx, st)
	if rerr != nil {
		return tb.errToast(c, "refresh", rerr)
	}
	body = tb.msg.ClearAllDone(n) + "\n\n" + body
	tb.editWithList(c, body, kb)
	return c.Respond()
}

func (tb *Bot) cbPendingClearNo(c tele.Context) error {
	cb := c.Callback()
	if cb == nil {
		return nil
	}
	body, kb, err := tb.renderPending(context.Background(), parsePendingState(cb.Data))
	if err != nil {
		return tb.errToast(c, "refresh", err)
	}
	tb.editWithList(c, body, kb)
	return c.Respond()
}
