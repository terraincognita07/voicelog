package telegram

import (
	"fmt"
	"strings"
)

// commandHint is one row for Telegram's /-menu (synced via bot.SetCommands).
// Command names are not localized — only the short description shown next to
// the slash in the blue menu.
type commandHint struct {
	Cmd  string
	Desc string
}

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
	Commands       []commandHint
	MenuPending    string
	MenuRecent     string
	MenuVocab      string
	MenuHelp       string
	VocabUsage     string
	VocabList      func(terms []string) string
	VocabAdded     func(added, total int) string
	VocabRemoved   func(term string, ok bool) string
	VocabClearAsk  string
	VocabCleared   func(n int) string
	VocabHeader    func(n int) string
	VocabRmBtn     func(term string) string
	VocabAddBtn    string
	VocabClearBtn  string
	VocabYesBtn    string
	VocabNoBtn     string
	VocabAddPrompt string

	ShowMoreBtn         string
	FilterAllBtn        string
	FilterPendingBtn    string
	FilterDiscardedBtn  string
	FilterActiveMark    string // prefix added to the currently active chip
	GoDiscardedBtn      string // jump to /recent with discarded filter (for empty lists)
}

var locales = map[string]messages{
	"en": {
		Help: "voicelog — send a voice message or audio file.\n\n" +
			"The buttons below the message are the primary path; the slash " +
			"commands are kept as a fallback / for batch use.\n\n" +
			"Buttons:\n" +
			"📋 Pending / 🕘 Recent — open the list with per-note 🗑/↩ actions\n" +
			"📒 Vocab — interactive vocabulary editor\n" +
			"🗑 under any saved-note reply — discard that note in one tap\n\n" +
			"Slash commands (fallback):\n" +
			"/pending /recent — same lists\n" +
			"/delete <id> — discard a note by id\n" +
			"/vocab add <term> ... — batch add\n" +
			"/vocab del <term> — remove one\n" +
			"/vocab clear confirm — wipe vocabulary",
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
		Commands: []commandHint{
			{"pending", "last 20 pending notes"},
			{"recent", "last 10 notes (any status)"},
			{"vocab", "manage whisper vocabulary"},
			{"delete", "mark a note as discarded"},
			{"help", "show commands"},
		},
		MenuPending: "📋 Pending",
		MenuRecent:  "🕘 Recent",
		MenuVocab:   "📒 Vocab",
		MenuHelp:    "❓ Help",
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
		VocabHeader: func(n int) string {
			if n == 0 {
				return "vocabulary is empty"
			}
			return fmt.Sprintf("vocabulary (%d):", n)
		},
		VocabRmBtn:     func(term string) string { return term + " ❌" },
		VocabAddBtn:    "➕ Add",
		VocabClearBtn:  "🗑 Clear",
		VocabYesBtn:    "✓ Yes, wipe",
		VocabNoBtn:     "✗ Cancel",
		VocabAddPrompt: "Send terms separated by spaces (reply to this message).",

		ShowMoreBtn:        "⤵ Show more",
		FilterAllBtn:       "All",
		FilterPendingBtn:   "Pending",
		FilterDiscardedBtn: "Discarded",
		FilterActiveMark:   "• ",
		GoDiscardedBtn:     "🕘 Show discarded",
	},
	"ru": {
		Help: "voicelog — шли голосовое или аудио.\n\n" +
			"Основной путь — кнопки под сообщением. Текстовые команды " +
			"оставлены как fallback / для batch.\n\n" +
			"Кнопки:\n" +
			"📋 Необработанные / 🕘 Последние — список с 🗑/↩ под каждой заметкой\n" +
			"📒 Словарь — интерактивный редактор\n" +
			"🗑 под ответом «✓ saved» — отбросить заметку в один тап\n\n" +
			"Текстовые команды (fallback):\n" +
			"/pending /recent — те же списки\n" +
			"/delete <id> — отбросить по id\n" +
			"/vocab add <term> ... — пакетное добавление\n" +
			"/vocab del <term> — удалить одно\n" +
			"/vocab clear confirm — очистить словарь",
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
		Commands: []commandHint{
			{"pending", "последние 20 необработанных"},
			{"recent", "последние 10 (любой статус)"},
			{"vocab", "словарь whisper"},
			{"delete", "пометить заметку как discarded"},
			{"help", "список команд"},
		},
		MenuPending: "📋 Необработанные",
		MenuRecent:  "🕘 Последние",
		MenuVocab:   "📒 Словарь",
		MenuHelp:    "❓ Помощь",
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
		VocabHeader: func(n int) string {
			if n == 0 {
				return "словарь пуст"
			}
			return fmt.Sprintf("словарь (%d):", n)
		},
		VocabRmBtn:     func(term string) string { return term + " ❌" },
		VocabAddBtn:    "➕ Добавить",
		VocabClearBtn:  "🗑 Очистить",
		VocabYesBtn:    "✓ Да, очистить",
		VocabNoBtn:     "✗ Отмена",
		VocabAddPrompt: "Пришли термины через пробел (ответом на это сообщение).",

		ShowMoreBtn:        "⤵ Показать ещё",
		FilterAllBtn:       "Все",
		FilterPendingBtn:   "Необработанные",
		FilterDiscardedBtn: "Отброшенные",
		FilterActiveMark:   "• ",
		GoDiscardedBtn:     "🕘 Показать отброшенные",
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
