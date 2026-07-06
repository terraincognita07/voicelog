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
		{Filter: "pending", Limit: 40, ExpDay: ""},
	}
	for _, in := range cases {
		got := parseRecentState(in.encode())
		if got != in {
			t.Errorf("recent roundtrip: in=%+v got=%+v (encoded=%q)", in, got, in.encode())
		}
	}
}

func TestCardRefRoundtrip(t *testing.T) {
	cases := []cardRef{
		{id: 42, kind: "p", state: pendingState{Limit: 20, ExpDay: "2026-05-26"}.encode()},
		{id: 7, kind: "r", state: recentState{Filter: "pending", Limit: 30, ExpDay: ""}.encode()},
	}
	for _, in := range cases {
		got, ok := parseCardRef(in.encode())
		if !ok || got != in {
			t.Errorf("cardRef roundtrip: in=%+v got=%+v ok=%v (encoded=%q)", in, got, ok, in.encode())
		}
	}
	// tag-remove carries an index + tag fingerprint alongside the ref.
	ref := cardRef{id: 9, kind: "r", state: "all:10:"}
	gotRef, idx, fp, ok := parseTagRemove(ref.encodeTagRemove(3, "идея"))
	if !ok || gotRef != ref || idx != 3 || fp != tagFingerprint("идея") {
		t.Errorf("tag-remove roundtrip: ref=%+v idx=%d fp=%q ok=%v", gotRef, idx, fp, ok)
	}
	// Garbage / bad kind is rejected.
	if _, ok := parseCardRef("notanint:p:"); ok {
		t.Error("non-numeric id must be rejected")
	}
	if _, ok := parseCardRef("5:x:state"); ok {
		t.Error("unknown kind must be rejected")
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
		_, _ = parseCardRef(c)
		_, _, _, _ = parseTagRemove(c)
		_, _ = parseListRef(c)
	}
}

func TestValidDateKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"2026-05-26", "2026-05-26"},     // happy path
		{"2026-02-28", "2026-02-28"},     // non-leap February valid
		{"2026-02-29", ""},               // not a leap year — invalid
		{"2024-02-29", "2024-02-29"},     // leap year — valid
		{"2026-13-99", ""},               // out-of-range month + day
		{"garbage", ""},                  // free-form garbage
		{"", ""},                         // empty stays empty
		{"  2026-05-26  ", "2026-05-26"}, // surrounding whitespace tolerated
		{"2026-5-26", ""},                // missing zero-padding rejected
		{"26-05-2026", ""},               // wrong order rejected
		{string([]byte{0xff, 0xfe}), ""}, // non-utf8 bytes
	}
	for _, c := range cases {
		if got := validDateKey(c.in); got != c.want {
			t.Errorf("validDateKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidRecentFilter(t *testing.T) {
	// Whitelist: only "pending" survives. Anything else — including
	// "discarded" (a now-removed status), "analyzed", "all", garbage,
	// mixed case — collapses to "" (= no filter).
	cases := []struct {
		in, want string
	}{
		{"pending", "pending"},
		{"discarded", ""},
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
		{-100, 1},                        // negative clamps up to 1
		{-1, 1},                          // -1 clamps up to 1
		{0, 1},                           // 0 clamps up to 1
		{1, 1},                           // 1 passes through
		{20, 20},                         // typical page size unchanged
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
		{"valid filter valid limit", "pending:25", recentState{Filter: "pending", Limit: 25}},
		{"valid filter valid limit invalid date", "pending:25:nope", recentState{Filter: "pending", Limit: 25}},
		{"full happy", "pending:25:2026-05-26", recentState{Filter: "pending", Limit: 25, ExpDay: "2026-05-26"}},
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
	// Telegram caps callback data at 64 bytes. The longest payloads we now
	// generate are the note-card refs: id + kind + the originating list state
	// (and, for tag removal, a tag index).
	rState := recentState{Filter: "pending", Limit: 99999, ExpDay: "2026-05-26"}.encode()
	ref := cardRef{id: 999999999999, kind: "r", state: rState}
	if got := len(ref.encode()); got > 64 {
		t.Errorf("cardRef.encode = %d bytes, exceeds Telegram 64-byte cap", got)
	}
	// Tag length doesn't matter — the payload carries a fixed-width
	// fingerprint, not the tag — but pass a long one to prove it.
	if got := len(ref.encodeTagRemove(99, "очень-длинный-тег-за-пределами-бюджета")); got > 64 {
		t.Errorf("cardRef.encodeTagRemove = %d bytes, exceeds Telegram 64-byte cap", got)
	}
	pState := pendingState{Limit: 99999, ExpDay: "2026-05-26"}.encode()
	pref := cardRef{id: 999999999999, kind: "p", state: pState}
	if got := len(pref.encode()); got > 64 {
		t.Errorf("pending cardRef.encode = %d bytes, exceeds Telegram 64-byte cap", got)
	}
}
