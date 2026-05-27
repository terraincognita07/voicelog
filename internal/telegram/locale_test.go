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
			if m.DiscardBtn == "" {
				t.Error("DiscardBtn is empty")
			}
			if m.DiscardedReply == nil || m.DiscardedReply(1) == "" {
				t.Error("DiscardedReply missing or returns empty")
			}
			if m.VocabUsage == "" {
				t.Error("VocabUsage is empty")
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
			if m.VocabClearAsk == "" {
				t.Error("VocabClearAsk is empty")
			}
			if m.VocabCleared == nil || m.VocabCleared(3) == "" {
				t.Error("VocabCleared missing or returns empty")
			}
			if len(m.Commands) == 0 {
				t.Error("Commands list is empty")
			}
			if m.MenuPending == "" || m.MenuRecent == "" || m.MenuVocab == "" || m.MenuHelp == "" {
				t.Error("Menu labels missing")
			}
			if m.VocabHeader == nil || m.VocabHeader(0) == "" || m.VocabHeader(3) == "" {
				t.Error("VocabHeader missing or returns empty")
			}
			if m.VocabRmBtn == nil || m.VocabRmBtn("x") == "" {
				t.Error("VocabRmBtn missing or returns empty")
			}
			if m.VocabAddBtn == "" || m.VocabClearBtn == "" || m.VocabYesBtn == "" || m.VocabNoBtn == "" {
				t.Error("Vocab inline buttons missing")
			}
			if m.VocabAddPrompt == "" {
				t.Error("VocabAddPrompt is empty")
			}
			for i, h := range m.Commands {
				if h.Cmd == "" || h.Desc == "" {
					t.Errorf("Commands[%d] has empty field: %+v", i, h)
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
