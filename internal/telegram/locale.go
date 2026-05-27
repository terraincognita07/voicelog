package telegram

import "fmt"

// messages is the set of user-visible strings the bot renders. Picked at
// startup via the BOT_LOCALE env var. Add a new locale by appending an
// entry to locales below — tests guarantee every locale has every field.
type messages struct {
	Help        string
	Recorded    func(id int64, pending int) string
	EmptyTrans  string
	EmptyList   string
	UsageDelete string
	BadID       string
	NotFound    func(id int64) string
	Discarded   func(id int64) string
	Error       func(label string, err error) string
}

var locales = map[string]messages{
	"en": {
		Help: "voicelog — send a voice message or audio file.\n\n" +
			"/pending — last 20 pending notes\n" +
			"/recent — last 10 notes (any status)\n" +
			"/delete <id> — mark as discarded",
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
	},
	"ru": {
		Help: "voicelog — шли голосовое или аудио.\n\n" +
			"/pending — последние 20 необработанных\n" +
			"/recent — последние 10 (любой статус)\n" +
			"/delete <id> — пометить как discarded",
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
