package telegram

import (
	"fmt"
	"strings"
)

// messages is the set of user-visible strings the bot renders. Picked at
// startup via the BOT_LOCALE env var. Add a new locale by appending an
// entry to locales below — tests guarantee every locale has every field.
type messages struct {
	Help           string
	Recorded       func(id int64, pending int) string
	EmptyTrans     string
	EmptyList      string
	UsageDelete    string
	BadID          string
	NotFound       func(id int64) string
	Discarded      func(id int64) string
	Error          func(label string, err error) string
	DiscardBtn     string
	DiscardedReply func(id int64) string
	VocabUsage     string
	VocabList      func(terms []string) string
	VocabAdded     func(added, total int) string
	VocabRemoved   func(term string, ok bool) string
	VocabClearAsk  string
	VocabCleared   func(n int) string
}

var locales = map[string]messages{
	"en": {
		Help: "voicelog — send a voice message or audio file.\n\n" +
			"/pending — last 20 pending notes\n" +
			"/recent — last 10 notes (any status)\n" +
			"/delete <id> — mark as discarded\n" +
			"/vocab — manage whisper vocabulary (names, jargon, rare terms)",
		Recorded: func(id int64, p int) string {
			return fmt.Sprintf("✓ saved #%d (%d pending)", id, p)
		},
		EmptyTrans:  "⚠ empty transcription",
		EmptyList:   "(empty)",
		UsageDelete: "usage: /delete <id>",
		BadID:       "id must be a number",
		NotFound: func(id int64) string {
			return fmt.Sprintf("not found #%d (or already discarded)", id)
		},
		Discarded: func(id int64) string {
			return fmt.Sprintf("✓ #%d → discarded", id)
		},
		Error: func(label string, err error) string {
			return fmt.Sprintf("⚠ %s: %v", label, err)
		},
		DiscardBtn: "🗑 Discard",
		DiscardedReply: func(id int64) string {
			return fmt.Sprintf("🗑 #%d discarded", id)
		},
		VocabUsage: "usage:\n" +
			"/vocab            — show current terms (same as 'list')\n" +
			"/vocab list       — show current terms\n" +
			"/vocab add <term> [<term> ...] — add one or more terms\n" +
			"/vocab del <term> — remove one term\n" +
			"/vocab clear      — ask to wipe everything\n" +
			"/vocab clear confirm — actually wipe everything",
		VocabList: func(terms []string) string {
			if len(terms) == 0 {
				return "vocabulary is empty"
			}
			return fmt.Sprintf("%d terms:\n%s", len(terms), strings.Join(terms, ", "))
		},
		VocabAdded: func(added, total int) string {
			if added == total {
				return fmt.Sprintf("✓ added %d", added)
			}
			return fmt.Sprintf("✓ added %d (%d already present)", added, total-added)
		},
		VocabRemoved: func(term string, ok bool) string {
			if ok {
				return fmt.Sprintf("✓ removed %q", term)
			}
			return fmt.Sprintf("%q not in vocabulary", term)
		},
		VocabClearAsk: "this will wipe the entire vocabulary. confirm with: /vocab clear confirm",
		VocabCleared: func(n int) string {
			return fmt.Sprintf("✓ wiped %d terms", n)
		},
	},
	"ru": {
		Help: "voicelog — шли голосовое или аудио.\n\n" +
			"/pending — последние 20 необработанных\n" +
			"/recent — последние 10 (любой статус)\n" +
			"/delete <id> — пометить как discarded\n" +
			"/vocab — словарь whisper (имена, жаргон, редкие термины)",
		Recorded: func(id int64, p int) string {
			return fmt.Sprintf("✓ записано #%d (%d pending)", id, p)
		},
		EmptyTrans:  "⚠ пустая транскрипция",
		EmptyList:   "пусто",
		UsageDelete: "использование: /delete <id>",
		BadID:       "id должен быть числом",
		NotFound: func(id int64) string {
			return fmt.Sprintf("не найдено #%d (или уже discarded)", id)
		},
		Discarded: func(id int64) string {
			return fmt.Sprintf("✓ #%d → discarded", id)
		},
		Error: func(label string, err error) string {
			return fmt.Sprintf("⚠ %s: %v", label, err)
		},
		DiscardBtn: "🗑 Отбросить",
		DiscardedReply: func(id int64) string {
			return fmt.Sprintf("🗑 #%d отброшено", id)
		},
		VocabUsage: "использование:\n" +
			"/vocab            — показать текущие термины (то же что 'list')\n" +
			"/vocab list       — показать текущие термины\n" +
			"/vocab add <термин> [<термин> ...] — добавить один или несколько\n" +
			"/vocab del <термин> — удалить один термин\n" +
			"/vocab clear      — запросить очистку всего словаря\n" +
			"/vocab clear confirm — реально очистить словарь",
		VocabList: func(terms []string) string {
			if len(terms) == 0 {
				return "словарь пуст"
			}
			return fmt.Sprintf("%d терминов:\n%s", len(terms), strings.Join(terms, ", "))
		},
		VocabAdded: func(added, total int) string {
			if added == total {
				return fmt.Sprintf("✓ добавлено %d", added)
			}
			return fmt.Sprintf("✓ добавлено %d (%d уже было)", added, total-added)
		},
		VocabRemoved: func(term string, ok bool) string {
			if ok {
				return fmt.Sprintf("✓ удалено %q", term)
			}
			return fmt.Sprintf("%q нет в словаре", term)
		},
		VocabClearAsk: "это удалит весь словарь. подтверди: /vocab clear confirm",
		VocabCleared: func(n int) string {
			return fmt.Sprintf("✓ удалено %d терминов", n)
		},
	},
}

// pickLocale returns the messages bundle for code, falling back to "en"
// if code is empty or unknown.
func pickLocale(code string) messages {
	if m, ok := locales[code]; ok {
		return m
	}
	return locales["en"]
}
