package telegram

import "testing"

func TestPendingStateRoundtrip(t *testing.T) {
	cases := []pendingState{
		{Limit: 20, ExpDay: ""},
		{Limit: 40, ExpDay: "2026-05-26"},
		{Limit: 1, ExpDay: ""},
	}
	for _, in := range cases {
		got := parsePendingState(in.encode())
		if got != in {
			t.Errorf("pending roundtrip: in=%+v got=%+v (encoded=%q)", in, got, in.encode())
		}
	}
}

func TestRecentStateRoundtrip(t *testing.T) {
	cases := []recentState{
		{Filter: "", Limit: 10, ExpDay: ""},
		{Filter: "pending", Limit: 30, ExpDay: "2026-05-26"},
		{Filter: "discarded", Limit: 40, ExpDay: ""},
	}
	for _, in := range cases {
		got := parseRecentState(in.encode())
		if got != in {
			t.Errorf("recent roundtrip: in=%+v got=%+v (encoded=%q)", in, got, in.encode())
		}
	}
}

func TestPendingStateWithID(t *testing.T) {
	st := pendingState{Limit: 20, ExpDay: "2026-05-26"}
	encoded := st.encodeWithID(42)
	gotID, gotState := parsePendingStateWithID(encoded)
	if gotID != 42 {
		t.Errorf("id: want 42, got %d", gotID)
	}
	if gotState != st {
		t.Errorf("state: want %+v, got %+v", st, gotState)
	}
}

func TestRecentStateWithID(t *testing.T) {
	st := recentState{Filter: "discarded", Limit: 30, ExpDay: "2026-05-26"}
	encoded := st.encodeWithID(42)
	gotID, gotState := parseRecentStateWithID(encoded)
	if gotID != 42 {
		t.Errorf("id: want 42, got %d", gotID)
	}
	if gotState != st {
		t.Errorf("state: want %+v, got %+v", st, gotState)
	}
}

func TestParseStateGarbageDoesNotPanic(t *testing.T) {
	// Crafted callback data must never panic and must fall back to defaults.
	cases := []string{
		"",
		":",
		"::",
		"garbage",
		"all:not_a_number:not_a_date",
		"42:",
		"42::",
		"42:0:" + string([]byte{0xff, 0xfe}),
	}
	for _, c := range cases {
		_ = parsePendingState(c)
		_ = parseRecentState(c)
		_, _ = parsePendingStateWithID(c)
		_, _ = parseRecentStateWithID(c)
	}
}

func TestValidDateKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"2026-05-26", "2026-05-26"},      // happy path
		{"2026-02-28", "2026-02-28"},      // non-leap February valid
		{"2026-02-29", ""},                // not a leap year — invalid
		{"2024-02-29", "2024-02-29"},      // leap year — valid
		{"2026-13-99", ""},                // out-of-range month + day
		{"garbage", ""},                   // free-form garbage
		{"", ""},                          // empty stays empty
		{"  2026-05-26  ", "2026-05-26"},  // surrounding whitespace tolerated
		{"2026-5-26", ""},                 // missing zero-padding rejected
		{"26-05-2026", ""},                // wrong order rejected
		{string([]byte{0xff, 0xfe}), ""},  // non-utf8 bytes
	}
	for _, c := range cases {
		if got := validDateKey(c.in); got != c.want {
			t.Errorf("validDateKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidRecentFilter(t *testing.T) {
	// Whitelist: only "pending" and "discarded" survive. Anything
	// else — including "analyzed", "all", garbage, mixed case —
	// collapses to "" (= no filter).
	cases := []struct {
		in, want string
	}{
		{"pending", "pending"},
		{"discarded", "discarded"},
		{"", ""},
		{"all", ""},
		{"analyzed", ""}, // valid db.Status but not a UI chip
		{"PENDING", ""},  // strict case sensitivity
		{"garbage", ""},
		{" pending ", ""}, // no implicit trim
	}
	for _, c := range cases {
		if got := validRecentFilter(c.in); got != c.want {
			t.Errorf("validRecentFilter(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClampPage(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{-100, 1},                // negative clamps up to 1
		{-1, 1},                  // -1 clamps up to 1
		{0, 1},                   // 0 clamps up to 1
		{1, 1},                   // 1 passes through
		{20, 20},                 // typical page size unchanged
		{maxListNotes, maxListNotes},     // exactly at cap is fine
		{maxListNotes + 1, maxListNotes}, // just past cap clamps down
		{99999, maxListNotes},            // far past cap clamps down
	}
	for _, c := range cases {
		if got := clampPage(c.in); got != c.want {
			t.Errorf("clampPage(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestParsePendingState_EdgeDefaults asserts that crafted callback
// data with bad fields falls back to canonical defaults instead of
// crashing or smuggling bad values into state.
func TestParsePendingState_EdgeDefaults(t *testing.T) {
	cases := []struct {
		name, in string
		want     pendingState
	}{
		{"empty", "", pendingState{Limit: pendingPageSize}},
		{"zero limit", "0:", pendingState{Limit: pendingPageSize}},
		{"negative limit", "-5:", pendingState{Limit: pendingPageSize}},
		{"non-int limit", "abc:", pendingState{Limit: pendingPageSize}},
		{"valid limit no date", "30:", pendingState{Limit: 30}},
		{"valid limit invalid date", "30:not-a-date", pendingState{Limit: 30}},
		{"valid limit valid date", "30:2026-05-26", pendingState{Limit: 30, ExpDay: "2026-05-26"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parsePendingState(c.in); got != c.want {
				t.Errorf("parsePendingState(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestParseRecentState_EdgeDefaults(t *testing.T) {
	cases := []struct {
		name, in string
		want     recentState
	}{
		{"empty", "", recentState{Limit: recentPageSize}},
		{"all explicit", "all", recentState{Limit: recentPageSize}},
		{"unknown filter", "garbage", recentState{Limit: recentPageSize}},
		{"valid filter no rest", "pending", recentState{Filter: "pending", Limit: recentPageSize}},
		{"valid filter zero limit", "pending:0", recentState{Filter: "pending", Limit: recentPageSize}},
		{"valid filter neg limit", "pending:-1", recentState{Filter: "pending", Limit: recentPageSize}},
		{"valid filter valid limit", "discarded:25", recentState{Filter: "discarded", Limit: 25}},
		{"valid filter valid limit invalid date", "discarded:25:nope", recentState{Filter: "discarded", Limit: 25}},
		{"full happy", "discarded:25:2026-05-26", recentState{Filter: "discarded", Limit: 25, ExpDay: "2026-05-26"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseRecentState(c.in); got != c.want {
				t.Errorf("parseRecentState(%q) = %+v, want %+v", c.in, got, c.want)
			}
		})
	}
}

func TestEncodeLimitsFit64Bytes(t *testing.T) {
	// Telegram caps callback data at 64 bytes. The longest realistic
	// payload we generate is an action button with state.
	st := recentState{Filter: "discarded", Limit: 99999, ExpDay: "2026-05-26"}
	if got := len(st.encodeWithID(999999999999)); got > 64 {
		t.Errorf("recent encodeWithID = %d bytes, exceeds Telegram 64-byte cap", got)
	}
	pst := pendingState{Limit: 99999, ExpDay: "2026-05-26"}
	if got := len(pst.encodeWithID(999999999999)); got > 64 {
		t.Errorf("pending encodeWithID = %d bytes, exceeds Telegram 64-byte cap", got)
	}
}
