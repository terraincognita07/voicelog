package telegram

import (
	"fmt"
	"strings"
	"time"
)

// commandHint is one row for Telegram's /-menu (synced via bot.SetCommands).
// Command names are not localized — only the short description shown next to
// the slash in the blue menu.
type commandHint struct {
	Cmd  string
	Desc string
}

// formatDuration renders a seconds count as M:SS (or 0:SS for sub-minute).
// Locale-neutral — used inside Recorded() in both en and ru.
func formatDuration(sec int) string {
	if sec < 0 {
		sec = 0
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

// messages is the set of user-visible strings the bot renders. Picked at
// startup via the BOT_LOCALE env var. Add a new locale by appending an
// entry to locales below — tests guarantee every locale has every field.
type messages struct {
	Welcome            string // shown on /start — short greeting + first-step hint
	Help               string // shown on /help — full guide
	Recorded           func(id int64, durSec int, pending int, preview string, suspect bool) string
	SuspectWarn        string                                    // appended to saved-reply when first segment looks like silence
	Duplicate          func(existingID int64, ageSec int) string // "duplicate of #N sent X sec ago"
	DiskFull           func(freeMB, minMB uint64) string         // capture refused — low disk
	EmptyTrans         string
	EmptyList          string
	EmptyPending       string // friendlier "(empty)" for /pending
	EmptyRecent        func(filter string) string
	EmptyVocab         string // shown in /vocab with [➕ Add] hint
	UsageDelete        string
	BadID              string
	NotFound           func(id int64) string
	Deleted            func(id int64) string // "🗑 #N permanently deleted."
	DeleteAsk          func(id int64) string // confirm prompt before an irreversible delete
	Errors             map[string]string
	ErrFallback        string
	DeleteBtn          string                                // [🗑 Delete] on a saved-note reply / list row
	DeleteYesBtn       string                                // confirm an irreversible delete
	DeleteNoBtn        string                                // cancel a delete
	ShowFullBtn        string                                // [📖 Show full] when preview was truncated
	EditBtn            string                                // [✏️ Edit] on a saved-note reply
	EditPrompt         func(id int64) string                 // force-reply prompt; MUST contain the id as its only number
	EditUpdated        func(id int64, preview string) string // confirmation after the text is replaced
	EditNotFound       func(find string) string              // find→replace: the "find" text wasn't in the note
	EditUsage          string                                // edit reply didn't parse / would empty the note
	Status             func(s string) string                 // localize "pending"/"analyzed"
	Transcribing       string                                // "transcribing..." flash before result
	Commands           []commandHint
	MenuPending        string
	MenuRecent         string
	MenuVocab          string
	MenuHelp           string
	VocabUsage         string
	VocabList          func(terms []string) string
	VocabAdded         func(added, total int) string
	VocabRemoved       func(term string, ok bool) string
	VocabClearAsk      func(n int) string
	VocabCleared       func(n int) string
	VocabHeader        func(n int) string
	VocabRmBtn         func(term string) string
	VocabAddBtn        string
	VocabClearBtn      string
	VocabYesBtn        string
	VocabNoBtn         string
	VocabAddPrompt     string
	VocabSkippedSuffix func(n int) string // " (skipped N too long)" — empty when n=0
	VocabClearFallback string             // text-mode hint when user typed "/vocab clear" without confirm

	ShowMoreBtn      string
	FilterAllBtn     string
	FilterPendingBtn string
	FilterActiveMark string // prefix added to the currently active chip

	ClearAllBtn    string
	ClearAllAsk    func(n int) string
	ClearAllYesBtn string
	ClearAllNoBtn  string
	ClearAllDone   func(n int) string

	DayToday     string
	DayYesterday string
	DayHeader    func(label string, count int) string // "📅 today (5)"
	DayLabel     func(t time.Time) string             // "Mon, May 26" / "Пн, 26 мая"
}

var locales = map[string]messages{
	"en": {
		Welcome: "👋 Welcome to voicelog — your self-hosted voice journal.\n\n" +
			"Record a voice message right now and I'll transcribe it. The buttons " +
			"at the bottom of the chat (📋 Pending / 🕘 Recent / 📒 Vocab / ❓ Help) " +
			"give you one-tap access to everything else.\n\n" +
			"Tap ❓ Help for the full guide.",
		Help: "voicelog · self-hosted voice journal\n\n" +
			"How to use:\n" +
			"1. Record a voice message — it's transcribed and saved as a note.\n" +
			"2. Use the buttons at the bottom of the chat to navigate:\n" +
			"   📋 Pending — fresh notes you haven't filed yet\n" +
			"   🕘 Recent — last 10 notes, filterable by status\n" +
			"   📒 Vocab — teach whisper names, jargon, rare terms\n" +
			"3. Under every saved-note reply: 🗑 to delete (asks first), ✏️ to fix the text.\n" +
			"4. In lists, tap 🗑 on a note to delete it (asks to confirm).\n\n" +
			"Power-user shortcuts (slash commands):\n" +
			"/pending /recent — open lists directly\n" +
			"/delete <id> — permanently delete a note by id\n" +
			"/vocab add <term> [<term>...] — batch add to vocabulary\n" +
			"/vocab del <term> — remove one term\n" +
			"/vocab clear confirm — wipe the vocabulary",
		Recorded: func(id int64, durSec int, p int, preview string, suspect bool) string {
			head := fmt.Sprintf("✓ Note #%d saved · %s · %d pending", id, formatDuration(durSec), p)
			out := head
			if preview != "" {
				out += "\n\n«" + preview + "»"
			}
			if suspect {
				out += "\n\n⚠ Looks like silence or non-speech — the transcription may be hallucinated. Review or 🗑."
			}
			return out
		},
		SuspectWarn: "⚠ Looks like silence or non-speech — the transcription may be hallucinated. Review or 🗑.",
		Duplicate: func(id int64, ageSec int) string {
			return fmt.Sprintf("🪞 Looks like a duplicate of note #%d (sent %d seconds ago). Skipped.", id, ageSec)
		},
		DiskFull: func(freeMB, minMB uint64) string {
			return fmt.Sprintf("⚠ Disk almost full (%d MB free, need at least %d MB). Capture refused — free up space on the bot host and try again.", freeMB, minMB)
		},
		EmptyTrans:   "⚠ Transcription came back empty — too quiet, too short, or non-speech audio.",
		EmptyList:    "Nothing here yet.",
		EmptyPending: "No pending notes. Record a voice message and it'll appear here.",
		EmptyRecent: func(filter string) string {
			switch filter {
			case "pending":
				return "No pending notes in the recent window."
			default:
				return "No notes yet. Record a voice message to get started."
			}
		},
		EmptyVocab:  "Vocabulary is empty.\nTap ➕ Add to teach whisper a name, jargon, or rare term.",
		UsageDelete: "Use /delete <id>, or tap 🗑 in /recent or /pending.",
		BadID:       "ID must be a number.",
		NotFound: func(id int64) string {
			return fmt.Sprintf("Note #%d not found.", id)
		},
		Deleted: func(id int64) string {
			return fmt.Sprintf("🗑 Note #%d permanently deleted.", id)
		},
		DeleteAsk: func(id int64) string {
			return fmt.Sprintf("🗑 Delete note #%d permanently? This can't be undone.", id)
		},
		Errors: map[string]string{
			"tmp dir":                "Couldn't prepare temporary storage.",
			"download from telegram": "Couldn't download your audio from Telegram.",
			"whisper":                "Speech recognition unavailable. Try again in a moment.",
			"insert note":            "Couldn't save the transcription.",
			"list pending":           "Couldn't load the pending list.",
			"list recent":            "Couldn't load the recent list.",
			"refresh":                "Couldn't refresh the view.",
			"delete":                 "Couldn't delete the note.",
			"clear":                  "Couldn't clear pending notes.",
			"edit note":              "Couldn't update the note text.",
			"vocab list":             "Couldn't load the vocabulary.",
			"vocab add":              "Couldn't add to vocabulary.",
			"vocab del":              "Couldn't remove from vocabulary.",
			"vocab clear":            "Couldn't clear the vocabulary.",
			"vocab rm":               "Couldn't remove that term.",
		},
		ErrFallback: "Something went wrong. Check the bot logs if it keeps happening.",
		Status: func(s string) string {
			switch s {
			case "pending":
				return "pending"
			case "analyzed":
				return "analyzed"
			}
			return s
		},
		Transcribing: "🎙 transcribing…",
		DeleteBtn:    "🗑 Delete",
		DeleteYesBtn: "✓ Yes, delete",
		DeleteNoBtn:  "✗ Cancel",
		ShowFullBtn:  "📖 Show full",
		EditBtn:      "✏️ Edit",
		EditPrompt: func(id int64) string {
			return fmt.Sprintf("✏️ Note #%d — reply with the full corrected text, or just a fix as «old → new».", id)
		},
		EditUpdated: func(id int64, preview string) string {
			head := fmt.Sprintf("✏️ Note #%d updated. The previous text is archived.", id)
			if preview == "" {
				return head
			}
			return head + "\n\n«" + preview + "»"
		},
		EditNotFound: func(find string) string {
			return fmt.Sprintf("🔍 «%s» not found in the note — nothing changed.", find)
		},
		EditUsage: "Send the full corrected text, or a replacement like «old → new».",
		Commands: []commandHint{
			{"pending", "last 20 pending notes"},
			{"recent", "last 10 notes (any status)"},
			{"vocab", "manage whisper vocabulary"},
			{"delete", "delete a note by id"},
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
		VocabClearAsk: func(n int) string {
			return fmt.Sprintf("Wipe all %d vocabulary terms?", n)
		},
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
		VocabSkippedSuffix: func(n int) string {
			if n == 0 {
				return ""
			}
			return fmt.Sprintf(" (skipped %d too long, limit %d chars)", n, MaxVocabTermLen)
		},
		VocabClearFallback: "Text fallback: /vocab clear confirm",

		ShowMoreBtn:      "⤵ Show more",
		FilterAllBtn:     "All",
		FilterPendingBtn: "Pending",
		FilterActiveMark: "• ",

		ClearAllBtn: "🗑 Delete all",
		ClearAllAsk: func(n int) string {
			return fmt.Sprintf("Delete all %d pending notes permanently? This can't be undone.", n)
		},
		ClearAllYesBtn: "✓ Yes, delete all",
		ClearAllNoBtn:  "✗ Cancel",
		ClearAllDone:   func(n int) string { return fmt.Sprintf("🗑 deleted %d pending notes", n) },

		DayToday:     "today",
		DayYesterday: "yesterday",
		DayHeader:    func(label string, count int) string { return fmt.Sprintf("📅 %s (%d)", label, count) },
		DayLabel:     func(t time.Time) string { return t.Format("Mon, Jan 2") },
	},
	"ru": {
		Welcome: "👋 Привет! Это voicelog — твой персональный голосовой журнал.\n\n" +
			"Запиши голосовое прямо сейчас — я расшифрую его. Кнопки внизу чата " +
			"(📋 Необработанные / 🕘 Последние / 📒 Словарь / ❓ Помощь) — " +
			"быстрый доступ ко всему остальному.\n\n" +
			"Тапни ❓ Помощь для полного гида.",
		Help: "voicelog · персональный голосовой журнал\n\n" +
			"Как пользоваться:\n" +
			"1. Запиши голосовое — оно расшифруется и сохранится как заметка.\n" +
			"2. Кнопки внизу чата:\n" +
			"   📋 Необработанные — свежие заметки, ещё не разобраны\n" +
			"   🕘 Последние — последние 10, с фильтром по статусу\n" +
			"   📒 Словарь — научи whisper именам, жаргону, редким терминам\n" +
			"3. Под каждой «✓ сохранено» — 🗑 удалить (спросит подтверждение), ✏️ исправить текст.\n" +
			"4. В списках тапни 🗑 у заметки, чтобы удалить (спросит подтверждение).\n\n" +
			"Команды для power-режима:\n" +
			"/pending /recent — открыть списки\n" +
			"/delete <id> — удалить заметку по id навсегда\n" +
			"/vocab add <термин> [<термин>...] — пакетное добавление\n" +
			"/vocab del <термин> — удалить один\n" +
			"/vocab clear confirm — очистить словарь",
		Recorded: func(id int64, durSec int, p int, preview string, suspect bool) string {
			head := fmt.Sprintf("✓ Заметка #%d сохранена · %s · в очереди %d", id, formatDuration(durSec), p)
			out := head
			if preview != "" {
				out += "\n\n«" + preview + "»"
			}
			if suspect {
				out += "\n\n⚠ Похоже на тишину или не речь — транскрипция может быть выдуманной. Проверь или 🗑."
			}
			return out
		},
		SuspectWarn: "⚠ Похоже на тишину или не речь — транскрипция может быть выдуманной. Проверь или 🗑.",
		Duplicate: func(id int64, ageSec int) string {
			return fmt.Sprintf("🪞 Похоже на дубль заметки #%d (отправлена %d сек назад). Пропускаю.", id, ageSec)
		},
		DiskFull: func(freeMB, minMB uint64) string {
			return fmt.Sprintf("⚠ Почти кончилось место (свободно %d MB, нужно минимум %d MB). Запись отклонена — освободи место на сервере и попробуй снова.", freeMB, minMB)
		},
		EmptyTrans:   "⚠ Транскрипция пустая — слишком тихо, коротко или не речь.",
		EmptyList:    "Пока ничего нет.",
		EmptyPending: "Очередь пуста. Запиши голосовое — заметка появится здесь.",
		EmptyRecent: func(filter string) string {
			switch filter {
			case "pending":
				return "В последних — нет необработанных."
			default:
				return "Заметок пока нет. Запиши голосовое, чтобы начать."
			}
		},
		EmptyVocab:  "Словарь пуст.\nНажми ➕ Добавить, чтобы научить whisper имени, жаргону или редкому термину.",
		UsageDelete: "Используй /delete <id> или тапни 🗑 в /recent или /pending.",
		BadID:       "ID должен быть числом.",
		NotFound: func(id int64) string {
			return fmt.Sprintf("Заметка #%d не найдена.", id)
		},
		Deleted: func(id int64) string {
			return fmt.Sprintf("🗑 Заметка #%d удалена навсегда.", id)
		},
		DeleteAsk: func(id int64) string {
			return fmt.Sprintf("🗑 Удалить заметку #%d навсегда? Это нельзя отменить.", id)
		},
		Errors: map[string]string{
			"tmp dir":                "Не удалось подготовить временное хранилище.",
			"download from telegram": "Не удалось скачать аудио из Telegram.",
			"whisper":                "Распознавание речи недоступно. Попробуй ещё раз.",
			"insert note":            "Не удалось сохранить транскрипцию.",
			"list pending":           "Не удалось загрузить очередь.",
			"list recent":            "Не удалось загрузить последние заметки.",
			"refresh":                "Не удалось обновить вид.",
			"delete":                 "Не удалось удалить заметку.",
			"clear":                  "Не удалось очистить очередь.",
			"edit note":              "Не удалось обновить текст заметки.",
			"vocab list":             "Не удалось загрузить словарь.",
			"vocab add":              "Не удалось добавить в словарь.",
			"vocab del":              "Не удалось удалить из словаря.",
			"vocab clear":            "Не удалось очистить словарь.",
			"vocab rm":               "Не удалось удалить термин.",
		},
		ErrFallback: "Что-то пошло не так. Если повторится — проверь логи бота.",
		Status: func(s string) string {
			switch s {
			case "pending":
				return "в очереди"
			case "analyzed":
				return "проанализирована"
			}
			return s
		},
		Transcribing: "🎙 распознаю…",
		DeleteBtn:    "🗑 Удалить",
		DeleteYesBtn: "✓ Да, удалить",
		DeleteNoBtn:  "✗ Отмена",
		ShowFullBtn:  "📖 Показать полностью",
		EditBtn:      "✏️ Исправить",
		EditPrompt: func(id int64) string {
			return fmt.Sprintf("✏️ Заметка #%d — ответь полным исправленным текстом или заменой «старое → новое».", id)
		},
		EditUpdated: func(id int64, preview string) string {
			head := fmt.Sprintf("✏️ Заметка #%d обновлена. Прежний текст сохранён в истории.", id)
			if preview == "" {
				return head
			}
			return head + "\n\n«" + preview + "»"
		},
		EditNotFound: func(find string) string {
			return fmt.Sprintf("🔍 «%s» не найдено в заметке — ничего не изменено.", find)
		},
		EditUsage: "Пришли полный исправленный текст или замену вида «старое → новое».",
		Commands: []commandHint{
			{"pending", "последние 20 необработанных"},
			{"recent", "последние 10 (любой статус)"},
			{"vocab", "словарь whisper"},
			{"delete", "удалить заметку по id"},
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
		VocabClearAsk: func(n int) string {
			return fmt.Sprintf("Удалить все %d терминов из словаря?", n)
		},
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
		VocabSkippedSuffix: func(n int) string {
			if n == 0 {
				return ""
			}
			return fmt.Sprintf(" (пропущено %d длиннее %d символов)", n, MaxVocabTermLen)
		},
		VocabClearFallback: "Текстовый fallback: /vocab clear confirm",

		ShowMoreBtn:      "⤵ Показать ещё",
		FilterAllBtn:     "Все",
		FilterPendingBtn: "Необработанные",
		FilterActiveMark: "• ",

		ClearAllBtn: "🗑 Удалить все",
		ClearAllAsk: func(n int) string {
			return fmt.Sprintf("Удалить все %d заметок из очереди навсегда? Это нельзя отменить.", n)
		},
		ClearAllYesBtn: "✓ Да, удалить все",
		ClearAllNoBtn:  "✗ Отмена",
		ClearAllDone:   func(n int) string { return fmt.Sprintf("🗑 удалено %d заметок", n) },

		DayToday:     "сегодня",
		DayYesterday: "вчера",
		DayHeader:    func(label string, count int) string { return fmt.Sprintf("📅 %s (%d)", label, count) },
		DayLabel:     formatDayRu,
	},
}

var ruWeekdays = [...]string{"Вс", "Пн", "Вт", "Ср", "Чт", "Пт", "Сб"}
var ruMonths = [...]string{
	"янв", "фев", "мар", "апр", "мая", "июн",
	"июл", "авг", "сен", "окт", "ноя", "дек",
}

// formatDayRu returns "Пн, 26 мая" — short weekday, day, short month
// (genitive case for May → "мая"; we use short forms to avoid full
// declensional gymnastics for every month).
func formatDayRu(t time.Time) string {
	wd := ruWeekdays[int(t.Weekday())]
	mo := ruMonths[int(t.Month())-1]
	return fmt.Sprintf("%s, %d %s", wd, t.Day(), mo)
}

// pickLocale returns the messages bundle for code, falling back to "en"
// if code is empty or unknown.
func pickLocale(code string) messages {
	if m, ok := locales[code]; ok {
		return m
	}
	return locales["en"]
}
