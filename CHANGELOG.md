# Changelog

All notable changes to voicelog will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning is loose [SemVer](https://semver.org/spec/v2.0.0.html): MAJOR
for breaking changes (schema migration that cannot roll forward, MCP tool
removal, env-var rename), MINOR for new features, PATCH for fixes.

## [Unreleased]

### Open

- F1 (MED) — concurrent migration race on fresh DB (`internal/db/db.go`).
- F2 (MED) — per-file migration not atomic (`internal/db/db.go`).
- F3 (MED) — audio janitor abandons files when `AUDIO_DIR` changes mid-life
  (`internal/audio/audio.go`).

## [0.3.0] — 2026-05-28

### Added

- **P2 batch (#16, #17, #18, #19):**
  - Audio dedup via SHA-256 hash within a 5-minute window. Migration 005
    adds `notes.audio_hash` + composite index. User gets a "duplicate of
    #N" reply instead of two identical notes.
  - Disk-full guard via `internal/diskguard.FreeBytes`. `MIN_FREE_DISK_MB`
    env (default 500). Bot refuses captures cleanly when free space is low.
  - DB maintenance loop in the mcp container: weekly `wal_checkpoint`,
    monthly `VACUUM`. `DB_MAINTENANCE_DISABLED` opts out.
  - `db_health` MCP tool — `integrity_check` + `quick_check` + counts +
    on-disk size. Read-only, safe to call ad-hoc.
- **P1 batch (#12, #13, #14, #15):**
  - Audio retention via `internal/audio`. Opt-in via
    `AUDIO_RETENTION_DAYS`. Background janitor every 6h.
  - Confidence + hallucination detection from whisper `verbose_json`.
    Migration 003 adds `notes.confidence_overall`, `confidence_min`,
    `suspect_hallucination`. `HALLUCINATION_THRESHOLD` env.
  - `retranscribe(id)` MCP tool. Migration 004 adds `notes_history` table.
    Re-runs whisper on retained audio, archives previous text.
- **Open-source documentation layer:** `CONTRIBUTING.md`, `ARCHITECTURE.md`,
  `CHANGELOG.md`, `.github/PULL_REQUEST_TEMPLATE.md`, `CODE_OF_CONDUCT.md`.
- `schema_migrations` tracker in `db.Migrate` — non-idempotent migrations
  (ALTER ADD COLUMN) run exactly once.

### Changed

- All raw `err.Error()` paths to the chat are now sanitized via
  `userErrMsg(label, err)`. Raw errs go to `slog.Error` only.
- `internal/telegram/bot.go` split into `bot.go` + `errors.go` +
  `saved_reply.go` + `list_view.go` + `vocab.go`.
- `telegram.New(...)` positional signature replaced by `telegram.Config`.
- `MCP search_notes` / `get_notes_in_range` now exclude `discarded` notes
  by default. Opt-in via `include_discarded=true`.

## [0.2.0] — 2026-05-27

### Added

- **P0 batch (#8, #9, #10, #11):**
  - `/vocab` Telegram command. Migration 002 adds `vocabulary` table.
    Whisper prompt = `WHISPER_PROMPT` env + user vocabulary.
  - Inline 🗑 button under saved-note replies. Two-way toggle (🗑 ↔ ↩).
  - MCP tools: `get_note`, `discard_notes`, `restore_note`.
  - FTS5 `snippet()` in `search_notes` responses.
- **Day-grouped list views** in `/pending` and `/recent` with
  collapsible days, status filter chips (`[All] [Pending] [Discarded]`),
  pagination, mass-discard confirm.
- **Persistent reply-keyboard** (`📋 Pending` `🕘 Recent` `📒 Vocab` `❓ Help`)
  attached on `/start` and `/help`. Synced command menu via `setMyCommands`.
- **i18n via `BOT_LOCALE`** (en default, ru opt-in). All user-facing strings
  through `internal/telegram/locale.go`.
- Day-of-week labels for older dates (`Mon, May 26` / `Пн, 26 мая`).
- `[📖 Show full]` button on truncated saved-reply previews.
- `[🗑 Clear all]` mass-discard for `/pending` with two-step confirm.
- `c.Notify(tele.Typing)` during transcription so the bot doesn't look
  silent on slow ffmpeg/whisper.
- Throttled rejection logging (1 entry per user-id per 15 min).
- govulncheck as a separate CI job, pinned to `v1.1.4`.

## [0.1.0] — 2026-05-26

Initial public release.

- Telegram bot + MCP server + whisper.cpp HTTP, three Docker containers.
- SQLite + FTS5 storage (migration 001).
- 4 MCP tools: `list_pending_notes`, `get_notes_in_range`, `search_notes`,
  `mark_analyzed`.
- `/pending`, `/recent`, `/delete <id>` text commands.
- README + SECURITY policy + LICENSE (MIT).
