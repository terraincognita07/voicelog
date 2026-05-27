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
	if validDateKey("2026-05-26") != "2026-05-26" {
		t.Error("valid ISO date should round-trip")
	}
	if validDateKey("2026-13-99") != "" {
		t.Error("invalid date components must collapse to empty")
	}
	if validDateKey("garbage") != "" {
		t.Error("garbage must collapse to empty")
	}
	if validDateKey("") != "" {
		t.Error("empty must stay empty")
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
