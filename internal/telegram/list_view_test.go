package telegram

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/terraincognita07/voicelog/internal/db"
	"github.com/terraincognita07/voicelog/internal/db/migrations"
)

// newTestBot wires the minimum bits of a *Bot for rendering tests:
// a real DB (temp file) and a locale. The *tele.Bot field stays nil
// because render methods only touch db + msg.
func newTestBot(t *testing.T) *Bot {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Migrate(ctx, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &Bot{
		db:     d,
		logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		msg:    pickLocale("en"),
	}
}

// calendarYesterday returns a Time on the *calendar* day before now, in
// now's location, at 23:00 local. Use this instead of
// `now.Add(-25*time.Hour)` when seeding "yesterday" notes — fixed-offset
// arithmetic slips into the day-before-yesterday whenever a test runs
// during the first hour of a new day, which used to flake
// TestRenderPending_MultiDayCollapsed.
func calendarYesterday(now time.Time) time.Time {
	y, m, d := now.Date()
	startOfToday := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	return startOfToday.Add(-1 * time.Hour)
}

// seedNoteAt inserts a note with a controlled created_at timestamp.
// Returns the new note ID.
func seedNoteAt(t *testing.T, b *Bot, when time.Time, text string) int64 {
	t.Helper()
	res, err := b.db.ExecContext(context.Background(),
		`INSERT INTO notes (created_at, raw_text, duration_sec) VALUES (?, ?, ?)`,
		when.Unix(), text, 5)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// keyboardTexts flattens an inline keyboard into a slice of button texts
// per row, useful for asserting the keyboard layout in a readable form.
func keyboardTexts(kb *tele.ReplyMarkup) [][]string {
	if kb == nil {
		return nil
	}
	out := make([][]string, len(kb.InlineKeyboard))
	for i, row := range kb.InlineKeyboard {
		out[i] = make([]string, len(row))
		for j, b := range row {
			out[i][j] = b.Text
		}
	}
	return out
}

// findRow returns the first row index where every cell matches the
// predicate, or -1.
func findRow(rows [][]string, pred func([]string) bool) int {
	for i, row := range rows {
		if pred(row) {
			return i
		}
	}
	return -1
}

// --- /pending -------------------------------------------------------------

func TestRenderPending_Empty(t *testing.T) {
	tb := newTestBot(t)
	body, kb, err := tb.renderPending(context.Background(), pendingState{Limit: pendingPageSize})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if body != tb.msg.EmptyPending {
		t.Errorf("empty body should be EmptyPending, got %q", body)
	}
	// Empty state must offer the [Show discarded] escape hatch.
	rows := keyboardTexts(kb)
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != tb.msg.GoDiscardedBtn {
		t.Errorf("empty keyboard should have [Show discarded] only, got %v", rows)
	}
}

func TestRenderPending_TodayOnly(t *testing.T) {
	tb := newTestBot(t)
	now := time.Now()
	id1 := seedNoteAt(t, tb, now.Add(-1*time.Minute), "first note")
	id2 := seedNoteAt(t, tb, now.Add(-30*time.Second), "second note")

	body, kb, err := tb.renderPending(context.Background(), pendingState{Limit: pendingPageSize})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Body must contain today's header and both notes.
	if !strings.Contains(body, "📅 "+tb.msg.DayToday) {
		t.Errorf("body missing today header: %q", body)
	}
	if !strings.Contains(body, "first note") || !strings.Contains(body, "second note") {
		t.Errorf("body missing note text: %q", body)
	}

	rows := keyboardTexts(kb)
	// Expect: action row with two trash buttons + [Clear all] row.
	wantTrash := []string{"🗑 #" + itoa(id2), "🗑 #" + itoa(id1)}
	if findRow(rows, func(r []string) bool { return stringSliceEq(r, wantTrash) }) < 0 {
		t.Errorf("missing action row %v; got %v", wantTrash, rows)
	}
	if findRow(rows, func(r []string) bool { return len(r) == 1 && r[0] == tb.msg.ClearAllBtn }) < 0 {
		t.Errorf("missing [Clear all] row; got %v", rows)
	}
}

func TestRenderPending_MultiDayCollapsed(t *testing.T) {
	tb := newTestBot(t)
	now := time.Now()
	seedNoteAt(t, tb, now.Add(-30*time.Second), "today1")
	// Calendar yesterday at 23:00 local. Subtracting a fixed 25h slid into
	// the day-before-yesterday when the test ran in the first hour of a
	// new day, breaking the "yesterday" group key.
	yesterday := calendarYesterday(now)
	seedNoteAt(t, tb, yesterday, "yesterday1")
	seedNoteAt(t, tb, yesterday.Add(-1*time.Minute), "yesterday2")

	body, kb, err := tb.renderPending(context.Background(), pendingState{Limit: pendingPageSize})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Today expanded → body shows today1; yesterday collapsed → body has
	// header with count but no note text.
	if !strings.Contains(body, "today1") {
		t.Errorf("today must be expanded in body: %q", body)
	}
	if strings.Contains(body, "yesterday1") || strings.Contains(body, "yesterday2") {
		t.Errorf("yesterday must be collapsed in body: %q", body)
	}
	if !strings.Contains(body, tb.msg.DayHeader(tb.msg.DayYesterday, 2)) {
		t.Errorf("body missing yesterday header w/ count: %q", body)
	}

	// Keyboard must offer a toggle button for yesterday.
	rows := keyboardTexts(kb)
	wantToggle := tb.msg.DayHeader(tb.msg.DayYesterday, 2)
	if findRow(rows, func(r []string) bool { return len(r) == 1 && r[0] == wantToggle }) < 0 {
		t.Errorf("missing day-toggle row %q; got %v", wantToggle, rows)
	}
}

func TestRenderPending_ExpandYesterday(t *testing.T) {
	tb := newTestBot(t)
	now := time.Now()
	seedNoteAt(t, tb, now.Add(-30*time.Second), "today1")
	yesterday := calendarYesterday(now)
	idY := seedNoteAt(t, tb, yesterday, "yesterday1")

	expDay := yesterday.Format("2006-01-02")
	body, kb, err := tb.renderPending(context.Background(), pendingState{Limit: pendingPageSize, ExpDay: expDay})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(body, "yesterday1") {
		t.Errorf("expanded yesterday must be in body: %q", body)
	}
	rows := keyboardTexts(kb)
	if findRow(rows, func(r []string) bool {
		for _, c := range r {
			if c == "🗑 #"+itoa(idY) {
				return true
			}
		}
		return false
	}) < 0 {
		t.Errorf("expanded day must have action button for its notes; got %v", rows)
	}
}

func TestRenderPending_ShowMoreAppears(t *testing.T) {
	tb := newTestBot(t)
	now := time.Now()
	// Seed 25 today notes; default page = 20, so "Show more" must appear.
	for i := 0; i < 25; i++ {
		seedNoteAt(t, tb, now.Add(-time.Duration(i)*time.Second), "n")
	}
	_, kb, err := tb.renderPending(context.Background(), pendingState{Limit: pendingPageSize})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rows := keyboardTexts(kb)
	if findRow(rows, func(r []string) bool { return len(r) == 1 && r[0] == tb.msg.ShowMoreBtn }) < 0 {
		t.Errorf("[Show more] must appear when more notes exist; got %v", rows)
	}
}

// --- /recent --------------------------------------------------------------

func TestRenderRecent_FilterChipsAlwaysPresent(t *testing.T) {
	tb := newTestBot(t)
	// Empty DB: chips still appear above empty body.
	_, kb, err := tb.renderRecent(context.Background(), recentState{Limit: recentPageSize})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rows := keyboardTexts(kb)
	if len(rows) == 0 {
		t.Fatal("empty /recent must still render the chip row")
	}
	chips := rows[0]
	if len(chips) != 3 {
		t.Fatalf("expected 3 filter chips, got %v", chips)
	}
}

func TestRenderRecent_DiscardedFilterShowsRestoreButtons(t *testing.T) {
	tb := newTestBot(t)
	now := time.Now()
	id := seedNoteAt(t, tb, now.Add(-1*time.Minute), "to be discarded")
	if err := tb.db.MarkDiscarded(context.Background(), id); err != nil {
		t.Fatalf("discard: %v", err)
	}

	body, kb, err := tb.renderRecent(context.Background(), recentState{Filter: "discarded", Limit: recentPageSize})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(body, "to be discarded") {
		t.Errorf("body must contain discarded note: %q", body)
	}
	// Active chip marked with bullet.
	rows := keyboardTexts(kb)
	if rows[0][2] != tb.msg.FilterActiveMark+tb.msg.FilterDiscardedBtn {
		t.Errorf("Discarded chip not marked active: %q", rows[0][2])
	}
	// Restore button (↩) must appear; trash must NOT for discarded note.
	want := "↩ #" + itoa(id)
	found := false
	for _, row := range rows {
		for _, c := range row {
			if c == want {
				found = true
			}
			if c == "🗑 #"+itoa(id) {
				t.Errorf("trash button should not appear for discarded note")
			}
		}
	}
	if !found {
		t.Errorf("missing restore button %q; got %v", want, rows)
	}
}

func TestRenderRecent_AllFilterShowsStatusInBody(t *testing.T) {
	tb := newTestBot(t)
	now := time.Now()
	seedNoteAt(t, tb, now.Add(-1*time.Minute), "pending one")
	body, _, err := tb.renderRecent(context.Background(), recentState{Filter: "", Limit: recentPageSize})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// Status word should be present in body when filter = all.
	if !strings.Contains(body, "["+tb.msg.Status("pending")+"]") {
		t.Errorf("All view must render status in body: %q", body)
	}
}

func TestRenderRecent_DiscardedFilterHidesStatusInBody(t *testing.T) {
	tb := newTestBot(t)
	now := time.Now()
	id := seedNoteAt(t, tb, now.Add(-1*time.Minute), "abc")
	_ = tb.db.MarkDiscarded(context.Background(), id)
	body, _, err := tb.renderRecent(context.Background(), recentState{Filter: "discarded", Limit: recentPageSize})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// When filter is set, body should NOT repeat the status word.
	if strings.Contains(body, "["+tb.msg.Status("discarded")+"]") {
		t.Errorf("filter-set view must not repeat status in body: %q", body)
	}
}

// --- helpers --------------------------------------------------------------

func itoa(n int64) string {
	return strings.TrimSpace(formatInt(n))
}

func formatInt(n int64) string {
	// Avoid importing strconv twice (already imported indirectly).
	if n == 0 {
		return "0"
	}
	var s []byte
	if n < 0 {
		s = append(s, '-')
		n = -n
	}
	start := len(s)
	for n > 0 {
		s = append(s, byte('0'+n%10))
		n /= 10
	}
	// Reverse the digits.
	for i, j := start, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return string(s)
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
