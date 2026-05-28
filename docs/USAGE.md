# Telegram bot — commands and UI

Reply language is selected via `BOT_LOCALE` (`en` default, `ru` opt-in);
commands themselves are not translated. Add more locales by appending to
the `locales` map in `internal/telegram/locale.go`.

## Capturing notes

- **Voice / audio message** — transcribed by whisper.cpp, stored as a note.
- **Plain text message** — stored verbatim as a note (no whisper). Use it
  when you can't speak (meeting, quiet place). Same saved-reply + `[🗑 Discard]`
  button as a voice note. (Text that is a `/command`, a menu-button tap, or a
  reply to the `/vocab` Add prompt is handled by those flows, not stored.)

## Commands

- `/pending` — last 20 pending notes (id, time, first 80 chars)
- `/recent` — last 10 notes regardless of status
- `/delete <id>` — mark a note as `discarded`
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
- **Inline 🗑 button** under every saved-note reply — one tap marks the
  just-recorded note as `discarded` without typing `/delete <id>`.
- **Inline ✏️ Edit button** next to 🗑 on a live note — tap it, the bot
  sends a force-reply prompt, reply with the corrected text and the note's
  `raw_text` is replaced (previous version archived to `notes_history`, so
  it's reversible at the SQL level). Useful when whisper misheard a word.
  Not offered on discarded notes (restore first).
- **Day-grouped lists** on `/pending` and `/recent`: notes are split into
  `📅 today (N)` / `📅 yesterday (N)` / `📅 2026-05-26 (N)` sections.
  Today is always expanded; older days are collapsed and shown as a
  single `[📅 date (N)]` button — tap to expand (one extra day at a time).
- **Status filter chips** at the top of `/recent`: `[All] [Pending]
  [Discarded]`; active chip prefixed with `•`. Discard/restore/pagination/
  day-expand all preserve the active filter.
- **Inline action buttons** for every visible note: `[🗑 #id]` (or
  `[↩ #id]` for discarded notes). Tap flips status and re-renders the
  list in place with the same filter, page, and expanded day.
- **`[⤵ Show more]`** grows the list by one page; capped at 40 notes per
  message to stay under Telegram's 4096-byte limit.
- **`[🗑 Clear all]`** under `/pending` mass-discards every pending note
  in one atomic UPDATE, with a two-step Yes/No confirm. Reversible per-note
  via the `[Discarded]` filter and `↩`.
- **Saved-reply two-way toggle.** After a voice message: `✓ Note #7 saved ·
  0:12 · 4 pending` plus a preview of the transcription, with `[🗑 Discard]`.
  Tap it → message becomes `🗑 Note #7 discarded · «preview»` with
  `[↩ Restore]`. Tap back to undo. No new messages spawned.
- **Sanitized errors.** Internal failures never leak to chat (no hostnames,
  paths, or third-party body content). Users see e.g. `⚠ Speech recognition
  unavailable. Try again in a moment.`; the full err lands in `slog`.
- **Day-grouped headers** use day-of-week for older days: `📅 Tue, May 26
  (3)` (localized when `BOT_LOCALE=ru`) — easier to skim than bare ISO dates.

The whisper "initial prompt" sent with each transcription is composed as
`WHISPER_PROMPT` (env, admin-default) followed by the `/vocab` terms.
