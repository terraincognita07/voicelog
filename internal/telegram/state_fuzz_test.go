package telegram

import "testing"

// Fuzz targets for the callback-data parsers. Telegram caps callback
// data at 64 bytes; the parsers must NEVER panic regardless of what's
// in those bytes (an attacker controls nothing here today, but the
// parsers are defensive in shape and we want to keep them so).
//
// The corpus also nails down the contract: every Parse function must
// produce a value with sane defaults (Limit ≥ 1, ExpDay either ""
// or a YYYY-MM-DD that round-trips through validDateKey).

func FuzzParsePendingState(f *testing.F) {
	for _, seed := range []string{
		"",
		":",
		"::",
		"garbage",
		"20:",
		"20:2026-05-26",
		"abc:2026-05-26",
		"-5:not-a-date",
		"99999999999999999999:2026-13-99",
		string([]byte{0x00, 0xff}),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := parsePendingState(in)
		if got.Limit < 1 {
			t.Errorf("Limit must be >= 1 after parse; got %d for %q", got.Limit, in)
		}
		// ExpDay invariant: either empty OR survives validDateKey.
		if got.ExpDay != "" && validDateKey(got.ExpDay) != got.ExpDay {
			t.Errorf("ExpDay %q from %q failed validDateKey round-trip", got.ExpDay, in)
		}
	})
}

func FuzzParseRecentState(f *testing.F) {
	for _, seed := range []string{
		"",
		"pending",
		"discarded:25:2026-05-26",
		"all:not-a-number:2026-13-99",
		"garbage:0:",
		string([]byte{0xff, 0xfe}),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := parseRecentState(in)
		if got.Limit < 1 {
			t.Errorf("Limit must be >= 1 after parse; got %d for %q", got.Limit, in)
		}
		// Filter invariant: only "" / "pending" / "discarded" allowed.
		switch got.Filter {
		case "", "pending", "discarded":
		default:
			t.Errorf("Filter must be whitelisted; got %q for %q", got.Filter, in)
		}
		if got.ExpDay != "" && validDateKey(got.ExpDay) != got.ExpDay {
			t.Errorf("ExpDay %q from %q failed validDateKey round-trip", got.ExpDay, in)
		}
	})
}

func FuzzParsePendingStateWithID(f *testing.F) {
	for _, seed := range []string{
		"",
		"42|20:",
		"42|20:2026-05-26",
		"abc|garbage",
		"-1|",
		string([]byte{0xff}),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		_, got := parsePendingStateWithID(in)
		if got.Limit < 1 {
			t.Errorf("Limit must be >= 1 after parse; got %d for %q", got.Limit, in)
		}
	})
}

func FuzzParseRecentStateWithID(f *testing.F) {
	for _, seed := range []string{
		"",
		"42|pending:25:",
		"42|discarded:25:2026-05-26",
		"abc|garbage",
		"-1|all:not-a-number:not-a-date",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		_, got := parseRecentStateWithID(in)
		if got.Limit < 1 {
			t.Errorf("Limit must be >= 1 after parse; got %d for %q", got.Limit, in)
		}
		switch got.Filter {
		case "", "pending", "discarded":
		default:
			t.Errorf("Filter must be whitelisted; got %q for %q", got.Filter, in)
		}
	})
}

func FuzzValidDateKey(f *testing.F) {
	for _, seed := range []string{
		"2026-05-26",
		"2024-02-29",
		"2026-02-29",
		"garbage",
		"",
		string([]byte{0xff, 0xfe}),
		"2026-13-99",
		"  2026-05-26  ",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := validDateKey(in)
		// Invariant: result is either "" or a YYYY-MM-DD that re-validates
		// to itself. No partial passthrough, no garbage in.
		if got == "" {
			return
		}
		if again := validDateKey(got); again != got {
			t.Errorf("not idempotent: %q → %q → %q", in, got, again)
		}
		if len(got) != 10 || got[4] != '-' || got[7] != '-' {
			t.Errorf("output not YYYY-MM-DD shape: %q (from %q)", got, in)
		}
	})
}

func FuzzClampPage(f *testing.F) {
	for _, seed := range []int{-100, -1, 0, 1, 20, maxListNotes, maxListNotes + 1, 99999} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in int) {
		got := clampPage(in)
		if got < 1 || got > maxListNotes {
			t.Errorf("clampPage(%d) = %d, must be in [1, %d]", in, got, maxListNotes)
		}
	})
}

func FuzzValidRecentFilter(f *testing.F) {
	for _, seed := range []string{
		"pending", "discarded", "", "all", "analyzed", "PENDING", "garbage",
		" pending ", string([]byte{0xff}),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got := validRecentFilter(in)
		switch got {
		case "", "pending", "discarded":
		default:
			t.Errorf("validRecentFilter(%q) = %q, must be whitelist or empty", in, got)
		}
	})
}
