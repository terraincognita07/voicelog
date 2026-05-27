package telegram

import (
	"errors"
	"testing"
)

func TestLocalesAreComplete(t *testing.T) {
	for code, m := range locales {
		t.Run(code, func(t *testing.T) {
			if m.Help == "" {
				t.Error("Help is empty")
			}
			if m.EmptyTrans == "" {
				t.Error("EmptyTrans is empty")
			}
			if m.EmptyList == "" {
				t.Error("EmptyList is empty")
			}
			if m.UsageDelete == "" {
				t.Error("UsageDelete is empty")
			}
			if m.BadID == "" {
				t.Error("BadID is empty")
			}
			if m.Recorded == nil || m.Recorded(1, 0) == "" {
				t.Error("Recorded missing or returns empty")
			}
			if m.NotFound == nil || m.NotFound(1) == "" {
				t.Error("NotFound missing or returns empty")
			}
			if m.Discarded == nil || m.Discarded(1) == "" {
				t.Error("Discarded missing or returns empty")
			}
			if m.Error == nil || m.Error("x", errors.New("y")) == "" {
				t.Error("Error missing or returns empty")
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
