# Telegram bot — commands and UI

Reply language is selected via `BOT_LOCALE` (`en` default, `ru` opt-in);
commands themselves are not translated. Add more locales by appending to
the `locales` map in `internal/telegram/locale.go`.

## Capturing notes

- **Voice / audio message** — transcribed by whisper.cpp, stored as a note.
- **Plain text message** — stored verbatim as a note (no whisper). Use it
  when you can't speak (meeting, quiet place). Same saved-reply + `[🗑 Delete]`
  button as a voice note. (Text that is a `/command`, a menu-button tap, or a
  reply to the `/vocab` Add prompt is handled by those flows, not stored.)

## Commands

- `/pending` — last 20 pending notes (id, time, first 60 chars)
- `/recent` — last 10 notes regardless of status
- `/delete <id>` — permanently delete a note by id (no confirm step — typing the id is itself the confirm)
- `/vocab` — manage whisper vocabulary (names, jargon, rare terms):
  - `/vocab` or `/vocab list` — interactive view with per-term `[term ❌]`
    buttons plus `[➕ Add]` and `[🗑 Clear]` at the bottom. `Add` opens a
    force-reply prompt; `Clear` shows a two-step inline confirm.
  - `/vocab add <term> [<term> ...]` — batch add via text (no UI)
  - `/vocab del <term>` — remove one term via text
  - `/vocab clear confirm` — text-form wipe (same effect as the inline confirm)
- `/help`, `/start` — show command list

## UI

- **Persistent menu** at the bottom of the chat: `Pending` / `Recent` /
  `Vocab` / `Help`. One-tap shortcut to the equivalent command.
- **Synced command menu** (blue button next to the input) — populated on
  startup via `setMyCommands`; no BotFather configuration needed.
- **Inline 🗑 Delete button** under every saved-note reply — tap it and the
  message swaps to a `Delete #N permanently?` confirm with `[✓ Yes, delete]` /
  `[✗ Cancel]`. Confirming erases the note, its edit history, and any retained
  audio file. There is no undo — that's the point of the confirm step.
- **Inline ✏️ Edit button** next to 🗑 — tap it and the message turns into an
  edit menu **in place** (no separate prompt to dangle): `[🔤 Replace a word]`
  walks you through *which word → with what* as plain questions (no syntax to
  remember — fixes one whisper-misheard word without retyping the note). If that
  word appears more than once, a numbered picker (each match shown with context)
  lets you replace just one occurrence or `Replace all`. `[📝 Rewrite all]`
  takes the whole new text, `[✗ Cancel]` restores the note. For a long note the
  menu shows a clipped preview with `[📖 Show full]` to expand it to the whole
  text in place — so you can see what you're changing before you change it.
  The note message is updated in place and the previous text is archived to
  `notes_history` (reversible at the SQL level). Back out at any point and
  nothing is changed or left hanging.
- **Tags (`🏷`)** show inline in `/pending` / `/recent` rows
  (`#9 22:04 · text…  🏷 идея, философия`). Claude sets them via MCP, and you
  can add/remove them by hand from a note's card (see below) — or ask Claude
  (`tag_note` / `untag_note`).
- **Day-grouped lists** on `/pending` and `/recent`: notes are split into
  `📅 today (N)` / `📅 yesterday (N)` / `📅 2026-05-26 (N)` sections.
  Today is always expanded; older days are collapsed and shown as a
  single `[📅 date (N)]` button — tap to expand (one extra day at a time).
- **Status filter chips** at the top of `/recent`: `[All] [Pending]`;
  active chip prefixed with `•`. Delete/pagination/day-expand all preserve
  the active filter.
- **Note card.** Each visible note has a `[#id]` button. Tap it to open the
  note's card — full text + tags with `[✏️ Edit]` `[🏷 Tags]` `[🗑 Delete]`
  `[⬅ To list]`. **Edit** and **Delete** behave like the saved-reply buttons;
  **Tags** opens a sub-view to add (reply with space-separated tags) or remove
  (`[tag ❌]`) tags by hand. `⬅ To list` returns to the exact view (filter,
  page, expanded day) you came from. This is how you edit/tag/delete notes
  recorded earlier — not just the just-recorded one.
- **`[⤵ Show more]`** grows the list by one page; capped at 40 notes per
  message to stay under Telegram's 4096-byte limit.
- **`[🗑 Delete all]`** under `/pending` permanently deletes every pending
  note in one atomic statement, behind a two-step Yes/No confirm. Not
  reversible.
- **Saved-reply.** After a voice message: `✓ Note #7 saved · 0:12 · 4
  pending` plus a preview of the transcription, with `[🗑 Delete]` and
  `[✏️ Edit]`. `🗑` asks to confirm before erasing; `✏️` edits the text.
  No new messages spawned.
- **Sanitized errors.** Internal failures never leak to chat (no hostnames,
  paths, or third-party body content). Users see e.g. `⚠ Speech recognition
  unavailable. Try again in a moment.`; the full err lands in `slog`.
- **Day-grouped headers** use day-of-week for older days: `📅 Tue, May 26
  (3)` (localized when `BOT_LOCALE=ru`) — easier to skim than bare ISO dates.

The whisper "initial prompt" sent with each transcription is composed as
`WHISPER_PROMPT` (env, admin-default) followed by the `/vocab` terms.
