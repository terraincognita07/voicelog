# Changelog

All notable changes to voicelog will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning is loose [SemVer](https://semver.org/spec/v2.0.0.html): MAJOR
for breaking changes (schema migration that cannot roll forward, MCP tool
removal, env-var rename), MINOR for new features, PATCH for fixes.

## [Unreleased]

### Added

- **Startup audio housekeeping** (bot only): after `Migrate`, the bot
  runs `audio.CheckDirPerm` (Warn when `AUDIO_DIR` exists with mode
  wider than `0700` — read-only, never chmods),
  `audio.RelativizeLegacyPaths` (one-shot normalization of pre-F3
  absolute `audio_path` rows that point under the current `AUDIO_DIR`),
  and `audio.ScanOrphans` (Warn-level log for `*.oga` files in
  `AUDIO_DIR` with no matching DB row). All three are read-mostly,
  idempotent, and skipped when retention is disabled. `CheckDirPerm`
  is gated by `runtime.GOOS != "windows"` (Windows reports synthetic
  perm bits).
- **Whisper warn-once for missing segments.** `whisper.Client` gains
  an optional `Logger` field (wired by `cmd/bot` and `cmd/mcp`). When
  a response decodes with no `segments[]`, the client emits a single
  `sync.Once`-guarded Warn per process lifetime — operators see that
  confidence detection is disabled (typical cause: whisper.cpp not
  returning `verbose_json`) without log spam. Tests:
  `TestTranscribeWAV_WarnsOnceOnMissingSegments`,
  `TestTranscribeWAV_DoesNotWarnOnSegmentsPresent`.
- **Audit batch in CI.** Two new jobs in
  `.github/workflows/ci.yml` complement the existing `govulncheck`
  gate. Both fail the PR on any finding:
  - `semgrep` — `p/golang` + `p/security-audit` rulesets, pinned via
    `semgrep/semgrep:1.95.0` container, `--error` exit code. Two
    pre-existing false-positives in `internal/db/notes.go` (the
    batch-IN `fmt.Sprintf(...placeholders)` pattern, where
    placeholders come from `len(ids)` and values bind through
    `ExecContext` args) are suppressed with inline
    `//nosemgrep:` comments + a short explanation.
  - `gitleaks` — `gitleaks/gitleaks-action@v2`, `fetch-depth: 0`
    so the scan sees every ancestor commit, not just `HEAD`.

  An `osv-scanner` job was tried and removed: for Go projects it
  produces non-actionable noise (every stdlib patch ships a fresh
  batch of OSV-indexed vulns; `osv-scanner` fails on all of them
  with no reachability filter). `govulncheck` already covers the
  same OSV database for Go with reachability analysis. The job
  stub left as a comment in `ci.yml` flags where to slot
  `osv-scanner` back in if a non-Go transitive ecosystem appears.
- **Open-source readiness batch:** `Makefile` with `make test /
  test-race / build / vet / lint / vuln / fmt / tidy / ci / clean`
  mirroring CI; `.editorconfig` for tab/space consistency;
  `docs/ROADMAP.md` (next / mid-term / speculative / won't-do
  buckets); `docs/RELEASING.md` (pre-release checklist, tag-cutting
  steps, and how to reconcile the CHANGELOG-vs-git mismatch from the
  v0.1.0 → v0.2.0 window); a Documentation section in README that
  wires ARCHITECTURE / CONTRIBUTING / CHANGELOG / SECURITY /
  CODE_OF_CONDUCT / ROADMAP into the front page; codecov badge
  (codecov upload was already in CI, just the badge was missing).

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
- **`retranscribe` refuses discarded notes.** Previously the MCP tool
  would silently overwrite a discarded note's text. Now it returns an
  error hinting the caller to `restore_note` first. Mirrors the
  "discarded = forget this" signal the bot's UI already respects.
  Covered by `TestRetranscribe_RefusesDiscarded`.
- **Telegram `c.Edit` failures are no longer swallowed.** The
  previous `_ = c.Edit(...)` callsites now route through
  `tb.tryEdit(...)`, which logs Warn on any error that's not
  Telegram's benign `"message is not modified"`. Same UX on the
  happy path; real failures (network drop, message-too-old, invalid
  markup) now appear in the slog stream.
- **`/vocab` clear-cancel asymmetry documented** in
  `internal/telegram/vocab.go`. No code change — the empty `Data`
  payload on Yes/No is intentional (vocab has no stateful view to
  preserve across the confirm modal) but the asymmetry with
  `/pending`'s clear-cancel had to be called out so a future
  contributor doesn't "fix" it by mistake.
- **`migrations/` moved to `internal/db/migrations/`.** The migration
  runner (`db.Migrate`) lives in `internal/db`; the SQL files it
  embeds now sit next to it, following Go's "package owns its
  resources" convention. Embed pattern stayed `*.sql` (relative).
  Seven importers swapped `voicelog/migrations` →
  `voicelog/internal/db/migrations`. Top-level root no longer has a
  `migrations/` directory.
- **`internal/mcp/server.go` split by tool family.** 542 → 128 lines
  in `server.go` (NewServer + shared helpers — `toMCP`, `jsonResult`,
  `toInt64Slice`, plus the `mcpNote` wire type). New same-package
  siblings: `tools_read.go` (230 — the five read-only tools),
  `tools_mutate.go` (114 — the three DB-writing tools that don't
  call out), `tools_retranscribe.go` (137 — `RetranscribeDeps`,
  `retranscribeResponse`, and the registrar that pulls in whisper +
  audio + promptbuilder). Public API unchanged.
- **`internal/db/notes.go` split by domain.** The 562-line file became
  five focused siblings under the same package: `notes.go` (321 — CRUD
  + `queryNotes` helper + Note / NoteMeta / Status / ErrNoteNotFound /
  MaxNotesInRange), `notes_search.go` (62 — FTS5 + `NoteWithRank`),
  `notes_audio.go` (115 — `SetAudioPath` / `ClearAudioPath` /
  `AudiosOlderThan` / `AllRetainedAudios` / `DupNote` /
  `FindRecentByHash`), `notes_history.go` (66 — `ArchiveAndUpdateText`),
  `notes_health.go` (40 — `HealthReport` / `Health`). All methods
  stayed on `*DB`; no caller change.
- **Dockerfiles relocated to `docker/`.** `Dockerfile.bot` and
  `Dockerfile.mcp` no longer sit at repo root. `docker-compose.yml`'s
  `dockerfile:` and `.github/workflows/ci.yml`'s `file:` updated to
  `docker/Dockerfile.{bot,mcp}`; build context stays at `.` so no
  COPY paths inside the Dockerfiles change. Removes two files of
  top-level clutter.
- **README split into `docs/`.** Top-level `README.md` went from
  658 to 249 lines — overview, Why-this-exists, the differentiation
  table, the Documentation index, Architecture, Quick start
  (sections 1–5 + Claude Code exposure), and License/Acknowledge-
  ments. Everything else moved under `docs/`:
  - `docs/CONFIG.md` — every env var, defaults, rotation guidance.
  - `docs/USAGE.md` — Telegram commands and inline UI.
  - `docs/MCP.md` — tool reference + Claude.ai web exposure
    (nginx and Traefik recipes, token-in-URL tradeoff).
  - `docs/SECURITY-MODEL.md` — threat model AND the "what is NOT
    protected" boundary (was the longest section in README).
  - `docs/OPERATIONS.md` — backups, model swap, DB maintenance,
    debugging the common-startup-failures list.
  - `docs/RUN-LOCALLY.md` — dev loop, project layout, Makefile
    targets, running without Docker.
  - `docs/ROADMAP.md` + `docs/RELEASING.md` cover roadmap and
    release process.
  External links to old README anchors will break — acceptable for
  a v0.1.x repo with no known external linkers.

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
- **Goroutine lifecycle tests** for `audio.Janitor` and
  `db.MaintenanceLoop`. Janitor immediate-return when retention is
  disabled (retentionDays ≤ 0 or dir==""); both goroutines exit
  within 2s of ctx cancel even though their tickers are configured
  for hours/days (cancel must unblock the select on `ctx.Done()`
  directly).
- **Migration bootstrap test** — `TestMigrateBootstrap` in
  `internal/db/db_test.go` verifies that after a fresh `Migrate`
  call the DB has every table, column, FTS5 trigger, and index that
  the codebase relies on, and that `schema_migrations` matches the
  list of `*.sql` files exactly. Catches "added a column in code,
  forgot the migration" regressions before the first INSERT.
- **`internal/diskguard` tests** — common test asserts
  `FreeBytes(t.TempDir())` returns `(non-zero, nil)` on every
  platform. Build-tagged tests cover the platform-specific surfaces:
  on Unix the value must be a real Statfs reading (`< MaxUint64`)
  and `FreeBytes("/no/such/path")` must surface the syscall error;
  on non-Unix `FreeBytes` must return the `MaxUint64` sentinel for
  any input. Removes the last `[no test files]` from
  `go test ./...`.

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
