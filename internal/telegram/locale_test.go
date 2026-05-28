package telegram

import (
	"strings"
	"testing"
	"time"
)

// TestLocalesAreComplete asserts every locale provides every user-facing
// field — protects against half-baked translations and missed renames.
func TestLocalesAreComplete(t *testing.T) {
	for code, m := range locales {
		t.Run(code, func(t *testing.T) {
			// Plain strings.
			for label, got := range map[string]string{
				"Welcome":            m.Welcome,
				"ShowFullBtn":        m.ShowFullBtn,
				"VocabClearFallback": m.VocabClearFallback,
				"Help":               m.Help,
				"EmptyTrans":         m.EmptyTrans,
				"EmptyList":          m.EmptyList,
				"EmptyPending":       m.EmptyPending,
				"EmptyVocab":         m.EmptyVocab,
				"UsageDelete":        m.UsageDelete,
				"BadID":              m.BadID,
				"DiscardBtn":         m.DiscardBtn,
				"RestoreBtn":         m.RestoreBtn,
				"EditBtn":            m.EditBtn,
				"MenuPending":        m.MenuPending,
				"MenuRecent":         m.MenuRecent,
				"MenuVocab":          m.MenuVocab,
				"MenuHelp":           m.MenuHelp,
				"VocabUsage":         m.VocabUsage,
				"VocabAddBtn":        m.VocabAddBtn,
				"VocabClearBtn":      m.VocabClearBtn,
				"VocabYesBtn":        m.VocabYesBtn,
				"VocabNoBtn":         m.VocabNoBtn,
				"VocabAddPrompt":     m.VocabAddPrompt,
				"ClearAllBtn":        m.ClearAllBtn,
				"ClearAllYesBtn":     m.ClearAllYesBtn,
				"ClearAllNoBtn":      m.ClearAllNoBtn,
				"GoDiscardedBtn":     m.GoDiscardedBtn,
				"ShowMoreBtn":        m.ShowMoreBtn,
				"FilterAllBtn":       m.FilterAllBtn,
				"FilterPendingBtn":   m.FilterPendingBtn,
				"FilterDiscardedBtn": m.FilterDiscardedBtn,
				"FilterActiveMark":   m.FilterActiveMark,
				"DayToday":           m.DayToday,
				"DayYesterday":       m.DayYesterday,
				"ErrFallback":        m.ErrFallback,
				"Transcribing":       m.Transcribing,
			} {
				if got == "" {
					t.Errorf("%s is empty", label)
				}
			}

			// Functions that take args and must return non-empty.
			if m.Recorded == nil ||
				m.Recorded(1, 12, 3, "hello", false) == "" ||
				m.Recorded(1, 0, 0, "", false) == "" ||
				m.Recorded(1, 5, 0, "hi", true) == "" {
				t.Error("Recorded missing or returns empty")
			}
			if !strings.Contains(m.Recorded(1, 5, 0, "hi", true), "⚠") {
				t.Errorf("Recorded(...suspect=true) must include the warning emoji; got %q", m.Recorded(1, 5, 0, "hi", true))
			}
			if m.NotFound == nil || m.NotFound(1) == "" {
				t.Error("NotFound missing or returns empty")
			}
			if m.Discarded == nil || m.Discarded(1) == "" {
				t.Error("Discarded missing or returns empty")
			}
			if m.DiscardedReply == nil || m.DiscardedReply(1, "x") == "" || m.DiscardedReply(1, "") == "" {
				t.Error("DiscardedReply missing or returns empty")
			}
			if m.RestoredReply == nil || m.RestoredReply(1, "x") == "" || m.RestoredReply(1, "") == "" {
				t.Error("RestoredReply missing or returns empty")
			}
			// EditPrompt must contain the id as its only number so onText can
			// recover it from a force-reply (see matchEditPrompt).
			if m.EditPrompt == nil || m.EditPrompt(42) == "" || !strings.Contains(m.EditPrompt(42), "42") {
				t.Error("EditPrompt missing, empty, or does not contain the id")
			}
			if m.EditUpdated == nil || m.EditUpdated(1, "x") == "" || m.EditUpdated(1, "") == "" {
				t.Error("EditUpdated missing or returns empty")
			}
			if m.VocabList == nil || m.VocabList(nil) == "" || m.VocabList([]string{"a"}) == "" {
				t.Error("VocabList missing or returns empty")
			}
			if m.VocabAdded == nil || m.VocabAdded(1, 1) == "" || m.VocabAdded(1, 3) == "" {
				t.Error("VocabAdded missing or returns empty")
			}
			if m.VocabRemoved == nil || m.VocabRemoved("x", true) == "" || m.VocabRemoved("x", false) == "" {
				t.Error("VocabRemoved missing or returns empty")
			}
			if m.VocabHeader == nil || m.VocabHeader(0) == "" || m.VocabHeader(3) == "" {
				t.Error("VocabHeader missing or returns empty")
			}
			if m.VocabRmBtn == nil || m.VocabRmBtn("x") == "" {
				t.Error("VocabRmBtn missing or returns empty")
			}
			if m.VocabClearAsk == nil || m.VocabClearAsk(3) == "" {
				t.Error("VocabClearAsk missing or returns empty")
			}
			if m.VocabCleared == nil || m.VocabCleared(3) == "" {
				t.Error("VocabCleared missing or returns empty")
			}
			if m.ClearAllAsk == nil || m.ClearAllAsk(5) == "" {
				t.Error("ClearAllAsk missing or returns empty")
			}
			if m.ClearAllDone == nil || m.ClearAllDone(3) == "" {
				t.Error("ClearAllDone missing or returns empty")
			}
			if m.EmptyRecent == nil {
				t.Error("EmptyRecent is nil")
			} else {
				for _, f := range []string{"", "pending", "discarded"} {
					if m.EmptyRecent(f) == "" {
						t.Errorf("EmptyRecent(%q) returned empty", f)
					}
				}
			}
			if m.Status == nil || m.Status("pending") == "" || m.Status("analyzed") == "" || m.Status("discarded") == "" {
				t.Error("Status missing or returns empty for known status")
			}
			if m.DayHeader == nil || m.DayHeader("today", 5) == "" {
				t.Error("DayHeader missing or returns empty")
			}
			if m.DayLabel == nil || m.DayLabel(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)) == "" {
				t.Error("DayLabel missing or returns empty")
			}
			if m.Duplicate == nil || m.Duplicate(7, 4) == "" {
				t.Error("Duplicate missing or returns empty")
			}
			if m.DiskFull == nil || m.DiskFull(40, 500) == "" {
				t.Error("DiskFull missing or returns empty")
			}
			if m.VocabSkippedSuffix == nil {
				t.Error("VocabSkippedSuffix is nil")
			} else {
				if got := m.VocabSkippedSuffix(0); got != "" {
					t.Errorf("VocabSkippedSuffix(0) must be empty, got %q", got)
				}
				if m.VocabSkippedSuffix(3) == "" {
					t.Errorf("VocabSkippedSuffix(3) must be non-empty")
				}
			}

			// Commands list.
			if len(m.Commands) == 0 {
				t.Error("Commands list is empty")
			}
			for i, h := range m.Commands {
				if h.Cmd == "" || h.Desc == "" {
					t.Errorf("Commands[%d] has empty field: %+v", i, h)
				}
			}

			// Errors map: must cover every label currently used by errReply
			// callsites, plus fall back gracefully on unknown labels.
			expectedErrLabels := []string{
				"tmp dir", "download from telegram", "whisper", "insert note",
				"list pending", "list recent", "refresh",
				"discard", "restore", "clear", "mark discarded", "edit note",
				"vocab list", "vocab add", "vocab del", "vocab clear", "vocab rm",
			}
			if m.Errors == nil {
				t.Fatal("Errors map is nil")
			}
			for _, label := range expectedErrLabels {
				if v, ok := m.Errors[label]; !ok || v == "" {
					t.Errorf("Errors[%q] missing or empty", label)
				}
			}
		})
	}
}

func TestPickLocaleFallback(t *testing.T) {
	if pickLocale("").Help != locales["en"].Help {
		t.Error("empty code should fall back to en")
	}
	if pickLocale("zz").Help != locales["en"].Help {
		t.Error("unknown code should fall back to en")
	}
	if pickLocale("ru").Help != locales["ru"].Help {
		t.Error("ru should return ru")
	}
}

// TestFormatDuration smoke-tests the seconds → M:SS helper used inside
// Recorded(). Not locale-specific, lives here next to the consumer.
func TestFormatDuration(t *testing.T) {
	cases := map[int]string{
		0:   "0:00",
		7:   "0:07",
		59:  "0:59",
		60:  "1:00",
		83:  "1:23",
		600: "10:00",
		-3:  "0:00", // defensive: negative collapses to zero
	}
	for in, want := range cases {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestFormatDayRu smoke-tests the Russian short-form day label.
func TestFormatDayRu(t *testing.T) {
	// 2026-05-26 was a Tuesday.
	got := formatDayRu(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC))
	if got != "Вт, 26 мая" {
		t.Errorf("formatDayRu = %q, want %q", got, "Вт, 26 мая")
	}
}
