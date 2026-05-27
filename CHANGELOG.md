# Changelog

All notable changes to voicelog will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning is loose [SemVer](https://semver.org/spec/v2.0.0.html): MAJOR
for breaking changes (schema migration that cannot roll forward, MCP tool
removal, env-var rename), MINOR for new features, PATCH for fixes.

## [Unreleased]

### Added

- **Startup audio housekeeping** (bot only): after `Migrate`, the bot
  runs `audio.RelativizeLegacyPaths` (one-shot normalization of
  pre-F3 absolute `audio_path` rows that point under the current
  `AUDIO_DIR`) and `audio.ScanOrphans` (Warn-level log for `*.oga`
  files in `AUDIO_DIR` with no matching DB row). Both are read-mostly,
  idempotent, and skipped when retention is disabled.

### Changed

- **`composePrompt` extracted to `internal/promptbuilder`.** Both the
  bot's live-transcription path and the MCP `retranscribe` tool now
  call `promptbuilder.Compose(ctx, src, basePrompt, logger)`. Unifies
  two minor behavior differences (bot now `TrimSpace`s the basePrompt;
  MCP now Warn-logs `VocabPrompt` errors). `VocabSource` is a minimal
  interface — `*db.DB` satisfies it implicitly.
- **`mustEnv` + `HALLUCINATION_THRESHOLD` parsing extracted to
  `internal/config`.** `cmd/bot` and `cmd/mcp` no longer carry
  identical copies of `mustEnv` or the float-in-[0,1] env parser.
  `config.ParseFloat01` keeps the existing per-binary defaults intact
  (bot still passes `0.0` as the "let telegram.New pick" signal, mcp
  still uses `0.6` directly).

### Tests

- **`whisper.transcribeWAV` HTTP coverage** via `httptest.NewServer`:
  verbose_json happy path, plain-json fallback (no segments), prompt
  presence/absence in multipart, HTTP error propagation, malformed
  JSON body, missing wav file. Outer `Transcribe` (with its ffmpeg
  pre-step) still depends on a real ffmpeg binary at test time.
- **`internal/mcp` integration tests** — 15 tests against a live
  `httptest.NewServer(mcp.BearerAuth(token, mcpHTTP))`. Cover bearer
  auth (missing / wrong / correct + WWW-Authenticate echo) plus a
  happy path for every tool: list_pending_notes, get_notes_in_range,
  search_notes, get_note (found + not_found), mark_analyzed,
  discard_notes, restore_note (discarded→pending + analyzed-not-
  restorable), retranscribe (unavailable when Whisper is nil),
  db_health. As prep, `bearerAuth` moved from `cmd/mcp/main.go` to
  `internal/mcp/auth.go` (exported as `BearerAuth`) so the test
  exercises the same wrapper that ships in production.

### Fixed

- **F1 + F2 (MED → resolved):** `db.Migrate` now wraps the full apply
  loop in `BEGIN IMMEDIATE` on a dedicated `*sql.Conn`. Concurrent
  runners (bot + mcp on a fresh DB) serialize on SQLite's RESERVED
  lock; per-file failures roll back together with the
  `schema_migrations` row so re-runs are clean. Guarded by
  `TestMigrateConcurrent` in `internal/db/db_test.go`.
- **F3 (MED → fully resolved):** `notes.audio_path` is written as a
  basename relative to `AUDIO_DIR` for new rows; legacy absolute rows
  that point under the current `AUDIO_DIR` are normalized at startup
  by `audio.RelativizeLegacyPaths`. Read sites (janitor, MCP
  retranscribe) go through `audio.Resolve`, which still accepts both
  formats as a backward-compat fallback. New `RetranscribeDeps.AudioDir`;
  `cmd/mcp` picks up `AUDIO_DIR` (same default as the bot).
- **Startup Open race on fresh DB (LOW → resolved):** `db.Open` now
  retries `PingContext` with exponential backoff (~3.15s budget) on
  `SQLITE_BUSY`. Two concurrent processes hitting a fresh DB file used
  to collide on the `journal_mode=WAL` pragma write before
  `busy_timeout` was effective; first deploy of bot+mcp under
  docker-compose could restart-loop. Distinct from F1/F2 (which were
  about `Migrate`); same user-visible symptom.

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
