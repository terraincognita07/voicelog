# Changelog

All notable changes to voicelog will be documented in this file.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning is loose [SemVer](https://semver.org/spec/v2.0.0.html): MAJOR
for breaking changes (schema migration that cannot roll forward, MCP tool
removal, env-var rename), MINOR for new features, PATCH for fixes.

## [Unreleased]

### Security

- **BOT_TOKEN no longer leaks into logs.** telebot embeds the token in every
  API URL (`/bot<TOKEN>/…`) and Go's `*url.Error` keeps that URL verbatim on
  transport failures, so an unsanitized error log (e.g. a failed `Download` or
  `Send`) could write the token to `docker logs`. All telebot-facing error
  sinks now redact it, and the bot installs a redacting `OnError` in place of
  telebot's default `log.Println` handler.
- **MCP HTTP server hardened.** Request bodies are capped
  (`http.MaxBytesHandler`, 1 MiB) so a valid-token POST can't OOM the process
  via mcp-go's `io.ReadAll`; added `ReadTimeout` and `IdleTimeout` (no
  `WriteTimeout` — retranscribe streams for minutes).
- **`.dockerignore` added.** Keeps `.env`, `data/*.db`, and local agent files
  out of the Docker build context, so a local operator build no longer bakes
  secrets/journal into the build-stage layer + BuildKit cache.

### Fixed

- **Data race in the button-driven edit flow.** A picker tap (`cbEditPick`)
  and a typed step (`continueEdit`) could mutate the same in-flight edit's
  fields concurrently (telebot dispatches each update on its own goroutine);
  `editMu` guarded only the pointer slot, not the fields. A new `editFlowMu`
  serializes the two flows; a concurrent regression test now exercises it
  under `-race`.
- **Graceful shutdown no longer drops an in-flight capture.** On SIGTERM the
  bot now stops the poller, drains in-flight voice/text captures (bounded, so
  a stuck whisper can't hang shutdown), then cancels the background loops —
  the note is persisted instead of lost with its already-confirmed Telegram
  offset. Fixed the defer ordering that closed the DB before stopping the
  janitor/maintenance loops.
- **Stale capture temp dirs are reclaimed.** A crash mid-transcription left
  `/tmp/voicelog-*` behind (the per-capture `defer` only runs on a clean
  return); startup now sweeps them.
- **MCP `search_notes` / `list_pending_notes` limits are clamped**
  server-side to `MaxNotesInRange` (500). A huge `limit` float no longer
  overflows `int()` into a negative value that SQLite treats as "no limit".

### Changed

- **MCP note objects always carry `confidence_overall`, `confidence_min`
  (null when unknown) and `suspect_hallucination` (bool).** Dropped the
  `omitempty` that silently omitted these, matching the `docs/MCP.md`
  contract; a decode-into-map test guards against re-drift.
- **CI: `staticcheck` pinned to `2026.1`** (first release with Go 1.26
  support), matching the pin-everything policy already applied to
  `govulncheck` and `semgrep`.

## [0.8.4] — 2026-06-09

### Changed

- Bump `modernc.org/sqlite` 1.51.0 → 1.52.0 (#24). Pure-Go SQLite driver
  patch update; no schema or API change.

## [0.8.3] — 2026-06-09

### Fixed

- **`retranscribe` no longer leaks a partial `.wav` on ffmpeg failure.**
  The temp WAV is removed even when conversion errors out. On the MCP
  retranscribe path the source lives under `/data/audio`, where the orphan
  scan matches only `*.oga` (not `*.oga.wav`), so a failed conversion used
  to leave an accumulating artifact.
- Docs: the `make build` row in `docs/RUN-LOCALLY.md` now shows the
  `-ldflags` version stamp (a plain `go build` reports `serverVersion="dev"`
  by design); clarified in this changelog that the `0.8.1` `serverVersion`
  hand-bump never reached shipped binaries.

## [0.8.2] — 2026-06-08

### Fixed

- **MCP `serverVersion` is now stamped from `git describe` at build
  time** instead of a hand-edited constant. The manual bump kept getting
  forgotten — the `v0.5.0` and `v0.8.1` tags both shipped binaries
  reporting the *previous* version in the MCP `initialize` handshake.
  `serverVersion` is now a `var` defaulting to `"dev"`, injected via
  `-ldflags` (wired in the Makefile and `docker/Dockerfile.mcp`); release
  images built after tagging report the tag automatically. See
  `docs/RELEASING.md`.
- **`delete_notes` removes retained audio even when whisper is unwired.**
  `cmd/mcp` only set `AudioDir` inside the `WHISPER_URL` branch, so a
  whisper-less mcp container silently left `.oga` files on disk despite
  the tool's "audio is removed" contract. `AudioDir` is now set
  unconditionally.

## [0.8.1] — 2026-06-01

### Changed

- **MCP tool errors are now sanitized.** Internal/store failures return a
  generic `"<tool> failed — see server logs"` instead of the raw error
  (which could carry SQL text or the DB file path), matching the bot's
  existing policy. Validation, not-found, and `search_notes` FTS5-query
  errors still return verbatim — they describe the caller's own input and
  are path-free, so they stay useful for fixing a query.
- **MCP `serverVersion` bumped `0.8.0` → `0.8.1`** to track the release
  tag. (Superseded — this hand-bump never reached shipped binaries; the
  constant kept drifting until `0.8.2` moved stamping to `git describe`.
  See `[0.8.2]`.)

### Fixed

- **Batch operations over large id lists no longer hit SQLite's variable
  limit.** `mark_analyzed` / `delete_notes` now chunk their id lists (500
  per batch), so a list past `SQLITE_MAX_VARIABLE_NUMBER` (~32k) no longer
  fails with "too many SQL variables".

## [0.8.0] — 2026-06-01

### Added

- **`📖 Show full` in the edit menu.** Opening `✏️ Edit` on a long note used to
  clip the preview to ~200 characters with no way to see the rest while
  editing. The menu now offers `📖 Show full`, which expands the note to its
  full text in place (same cap as the note card) and drops the button — so you
  can read the whole message before tapping `🔤 Replace a word` / `📝 Rewrite
  all`. Short notes that already fit are unchanged.

### Changed

- **MCP `serverVersion` bumped `0.7.0` → `0.8.0`** to track the release tag
  (RELEASING.md step 8). No MCP behavior change this release.

### Fixed

- **Adding a tag returns you to the tags menu.** Replying to the `➕ Add` tag
  prompt used to leave a bare `🏷 Added N tag(s).` line with no way back; the
  reply now re-renders the note's tags sub-view (updated tag list + `➕ Add` /
  `⬅ Back`), matching the `/vocab` add flow.

## [0.7.0] — 2026-05-31

### Added

- **Replace-a-word can target one occurrence of many.** When the word or phrase
  you're replacing appears more than once in a note, `🔤 Replace a word` now
  opens a numbered picker — each match shown with surrounding context (the match
  wrapped in `‹ ›`) and its own `[n]` button, plus `[Replace all]` and
  `[✗ Cancel]`. Tap one to change just that occurrence; previously every match
  was rewritten with no way to pick. A single match goes straight to the
  replacement as before. The list is capped at 6 matches (beyond that, only
  `Replace all`).

### Changed

- **MCP `serverVersion` bumped `0.6.0` → `0.7.0`** to track the release tag
  (RELEASING.md step 8). No MCP behavior change this release.

### Fixed

- **A half-finished edit no longer leaks into the next action.** The button edit
  flow keeps its progress in one in-memory slot that only `✗ Cancel`, a fresh
  `✏️`, or finishing the edit used to clear. Tapping `🏷 Tags → ➕ Add`,
  `/vocab → ➕ Add`, `🗑 Delete`, or sending a new voice/audio note while a
  `🔤`/`📝` edit was half-armed left that slot live — so the next message you
  typed was silently applied as an edit, and could even spill into a brand-new
  note. Every one of those pivots now abandons the pending edit. Viewing a note
  (opening its card, paging a list) intentionally keeps the edit, so you can
  reference another note before answering.

## [0.6.0] — 2026-05-31

### Changed

- **Note editing is now button-driven — the `old → new` arrow syntax is gone.**
  Tapping `✏️ Edit` (on a saved-note reply or a note card) turns that message
  into an in-place menu: `[🔤 Replace a word]` asks *which word → with what* as
  two plain prompts, `[📝 Rewrite all]` takes the full new text, `[✗ Cancel]`
  restores the note. The note message is updated **in place** (the old reply no
  longer lingers with stale text) and the typed answers are deleted to keep the
  chat clean. This replaces the previous force-reply flow, where a fix had to be
  typed as `old → new` and silently overwrote the **whole** note when the arrow
  was mistyped (e.g. `word->fix` without surrounding spaces). Text is still
  archived to `notes_history`. Internal: drops `splitEdit` / `matchEditPrompt`
  and the `EditUsage` string; `✏️` now opens `cbEditOpen`.
- **MCP `serverVersion` bumped `0.5.1` → `0.6.0`** to track the release tag
  (RELEASING.md step 8). No MCP behavior change this release.

## [0.5.1] — 2026-05-31

### Fixed

- **`serverVersion` now matches the release tag.** The `v0.5.0` tag landed on
  a commit that still reported `serverVersion` `0.4.0` in the MCP `initialize`
  handshake — the sync bump came one commit later (pushed to `main` but never
  tagged). `0.5.1` corrects the advertised version to `0.5.1`. Cosmetic: the
  version string only, no behavior change.

## [0.5.0] — 2026-05-31

### Changed (breaking)

- **`discarded` soft-delete replaced by permanent delete.** The bot's 🗑 and
  the MCP tools no longer park a note in a hidden `discarded` state — they
  erase it: the row, its edit history (`notes_history`), and any retained
  `.oga` audio file are removed for good. Irreversible.
  - **Bot:** 🗑 on a saved-note reply and on `/pending` / `/recent` rows now
    asks `Delete #N permanently?` (`[✓ Yes, delete]` / `[✗ Cancel]`) before
    erasing. The `↩ Restore` button and the `[Discarded]` filter chip are
    gone; `/delete <id>` and `[🗑 Delete all]` delete for good.
  - **MCP (contract change):** `discard_notes` → **`delete_notes`** (returns
    `{deleted: N}`, `destructive` hint, removes audio too). `restore_note`
    **removed**. The `include_discarded` parameter and the `discarded` status
    value are gone from `search_notes` / `get_notes_in_range`. `retranscribe`
    no longer carries a discarded guard. `serverVersion` 0.3.0 → 0.5.0; tool
    count 12 → 11.
  - **Migration `006_drop_discarded.sql`** deletes any rows still parked in
    `discarded` on the first start of the new version (their audio is
    reclaimed by the startup orphan scan / janitor). The `status` CHECK still
    lists `discarded` — rewriting it would need a full table rebuild — but no
    code path writes it anymore. **No rollback for already-deleted rows**
    beyond a database backup.

### Added

- **Note tags.** Notes can carry free-form category labels (`#идея`, `#todo`,
  `#философия`) — an analysis-side overlay that complements full-text search.
  A tag captures a *judgment that isn't in the note's words*, so
  `notes_by_tag("философия")` finds every philosophical note even when the
  word itself never appears. Four new MCP tools — `tag_note(id, tags[])`,
  `untag_note(id, tag)`, `list_tags()`, `notes_by_tag(tag)` — let Claude label
  the corpus and pull deterministic selections; every note object now carries
  a `tags` field. The bot shows `🏷` tags inline in `/pending` and `/recent`.
  Storage is a separate `note_tags` table (migration `007_tags.sql`, FK
  `ON DELETE CASCADE`), so deleting a note drops its tags too. MCP tool count
  11 → 15.
- **Find→replace editing.** The bot's `✏️ Edit` now accepts an `old → new`
  reply (separators `→` / `->` / `=>`) in addition to full-text replacement:
  it swaps every occurrence of `old` in the note, so fixing a single
  whisper-misheard word no longer means retyping the whole transcript. The
  previous text is still archived to `notes_history`.
- **Note card — edit/tag/delete any existing note from the lists.** Tapping a
  note in `/pending` or `/recent` now opens a card (full text + tags) with
  `[✏️ Edit] [🏷 Tags] [🗑 Delete] [⬅ To list]`, so editing, tagging and
  deleting are no longer limited to the just-recorded saved-reply. The Tags
  sub-view adds tags (reply with space-separated tags) and removes them
  (`[tag ❌]`) right in the bot. The per-note list button changed from
  `[🗑 #id]` (immediate delete-confirm) to `[#id]` (open card); deleting from
  a list is now one tap deeper, behind the same confirm.

## [0.3.0] — 2026-05-28

### Added

- Vocabulary management over MCP. Three new tools — `list_vocab`,
  `add_vocab(terms[])`, `remove_vocab(term)` — let Claude close the
  transcription-quality loop it's uniquely positioned to see: scanning the
  whole corpus it can spot whisper consistently mangling a name/term and add
  it to the vocabulary so future transcriptions improve. Per-term length
  capped at 64 chars. `clear_vocab` is intentionally NOT exposed (wiping the
  list stays a human two-step confirm in the bot). MCP tool count: 9 → 12.
- Edit a note's text from Telegram. Live saved-note replies now carry an
  `[✏️ Edit]` button next to `[🗑 Discard]`: tap it, reply to the force-reply
  prompt with the corrected text, and the note's `raw_text` is replaced. The
  previous version is archived to `notes_history` (reversible at the SQL
  level). Not offered on discarded notes. Fixes the "whisper misheard a word"
  case without needing the MCP `retranscribe` tool or retained audio.
- Plain-text note capture. Sending the bot a normal text message now stores
  it as a note (no whisper, duration 0), with the same saved-reply and
  `[🗑 Discard]` button a voice note gets — for logging when you can't speak.
  Commands, menu-button taps, and the `/vocab` Add force-reply are unaffected.
- Russian-aware full-text search. `search_notes` now stems bare Cyrillic
  query words (Snowball Russian) and prefix-matches them, so searching the
  dictionary form (`работа`) finds inflected forms in the corpus
  (`работе`, `работу`). Latin terms are matched exactly — English precision
  is unchanged. Query-side only: no migration, the FTS5 index stays plain
  `unicode61`. New dependency: `github.com/kljensen/snowball` (pure Go, MIT).

### Fixed

- MCP server reports its real version. `serverVersion` in
  `internal/mcp/server.go` had been stuck at `0.1.0` (the value it
  advertises to clients in the initialize handshake); it now tracks the
  release tag (`0.3.0` here). A release-checklist step in
  `docs/RELEASING.md` keeps it in sync instead of silently lagging.
- `scripts/smoke-mcp.sh` per-tool checks now actually run. They were
  invoked through `bash -c` subshells, which don't inherit the script's
  shell functions or non-exported vars — so 5 of 8 checks died with
  `call_tool: command not found` before sending any request. Fixed by
  exporting the helper functions + `MCP_URL`/`MCP_TOKEN`. The auth and
  tools/list checks were unaffected (plain in-process functions). Caught
  on the first real run against a live server.

## [0.2.1] — 2026-05-28

### Fixed

- **Module path is now the canonical `github.com/terraincognita07/voicelog`**
  (was a bare `module voicelog`). The bare path is not fetchable through
  the Go module proxy, which broke two things the repo already advertised:
  the Go Report Card badge (`proxy.golang.org/voicelog/@latest` → 404) and
  any `go install github.com/terraincognita07/voicelog/cmd/{bot,mcp}@latest`.
  All 22 internal import sites were rewritten from `voicelog/internal/...`
  to the full path. Purely mechanical — no runtime behavior change, no
  go.sum change, Docker builds unaffected (they compile from source). Not
  a breaking change in practice: the old bare path was never importable by
  anyone outside the module. Requires this tagged release for the proxy
  `@latest` to resolve with the corrected path.

## [0.2.0] — 2026-05-28

Sweep release covering everything that landed between `v0.1.0` and
`v0.2.0`. Reconciles the previously-drafted-but-never-tagged `[0.2.0]`
and `[0.3.0]` CHANGELOG sections (option A from
[docs/RELEASING.md](docs/RELEASING.md): one tag per CHANGELOG
heading). The original P0 / P1 / P2 / P3 batch framing is preserved
inside each subsection for readability — the work happened in waves,
but it ships as one release.

### Added

#### P0 — Telegram UX + initial MCP shape (originally drafted as v0.2.0)

- **`/vocab` Telegram command.** Migration 002 adds `vocabulary` table.
  Whisper prompt = `WHISPER_PROMPT` env + user vocabulary. Inline
  editor (term buttons + Add / Clear), text-mode fallback
  (`/vocab list|add|del|clear`), force-reply Add flow.
- **Inline 🗑 button under saved-note replies.** Two-way toggle
  (🗑 ↔ ↩) on the same message so a typo can be undone in one tap.
- **MCP tools added:** `get_note`, `discard_notes`, `restore_note`.
- **FTS5 `snippet()`** in `search_notes` responses — ~30 tokens of
  surrounding context with the matched term wrapped in `<<` `>>`.
- **Day-grouped list views** in `/pending` and `/recent` with
  collapsible days, status filter chips (`[All] [Pending]
  [Discarded]`), pagination, and mass-discard confirm.
- **Persistent reply-keyboard** (`📋 Pending` `🕘 Recent` `📒 Vocab`
  `❓ Help`) attached on `/start` and `/help`. Synced command menu
  via `setMyCommands`.
- **i18n via `BOT_LOCALE`** (en default, ru opt-in). All user-facing
  strings go through `internal/telegram/locale.go`.
- Day-of-week labels for older dates (e.g. `Mon, May 26`; localized
  under `BOT_LOCALE=ru`).
- `[📖 Show full]` button on truncated saved-reply previews.
- `[🗑 Clear all]` mass-discard for `/pending` with two-step confirm.
- `c.Notify(tele.Typing)` during transcription so the bot doesn't
  look silent on slow ffmpeg / whisper.
- Throttled rejection logging (1 entry per user-id per 15 min).
- `govulncheck` as a separate CI job, pinned to `v1.1.4`.

#### P1 — audio retention, quality signals, retranscribe (originally drafted as v0.3.0)

- **Audio retention via `internal/audio`.** Opt-in via
  `AUDIO_RETENTION_DAYS`. Background janitor every 6h.
- **Confidence + hallucination detection** from whisper `verbose_json`.
  Migration 003 adds `notes.confidence_overall`, `confidence_min`,
  `suspect_hallucination`. `HALLUCINATION_THRESHOLD` env knob.
- **`retranscribe(id)` MCP tool.** Migration 004 adds `notes_history`
  table. Re-runs whisper on retained audio, archives previous text.

#### P2 — dedup, disk-full, DB maintenance, db_health (originally drafted as v0.3.0)

- **Audio dedup via SHA-256 hash within a 5-minute window.**
  Migration 005 adds `notes.audio_hash` + composite index. User gets
  a "duplicate of #N" reply instead of two identical notes. Race
  between the SHA check and the INSERT is closed in this release
  (see Fixed).
- **Disk-full guard via `internal/diskguard.FreeBytes`.**
  `MIN_FREE_DISK_MB` env (default 500). Bot refuses captures cleanly
  when free space drops below the threshold.
- **DB maintenance loop in the mcp container:** weekly
  `wal_checkpoint`, monthly `VACUUM`. `DB_MAINTENANCE_DISABLED` opts
  out.
- **`db_health` MCP tool** — `integrity_check` + `quick_check` +
  counts + on-disk size. Read-only, safe to call ad-hoc. Gains a
  `quick=true` parameter (see Changed) to skip the slow integrity
  scan on multi-GB DBs.

#### P3+ — security, audit, operations hardening (the [Unreleased] batch)

- **Startup audio housekeeping** (bot only): after `Migrate`, the bot
  runs `audio.CheckDirPerm` (Warn when `AUDIO_DIR` exists with mode
  wider than `0700` — read-only, never chmods),
  `audio.RelativizeLegacyPaths` (one-shot normalization of pre-F3
  absolute `audio_path` rows that point under the current
  `AUDIO_DIR`), and `audio.ScanOrphans` (Warn-level log for `*.oga`
  files in `AUDIO_DIR` with no matching DB row). All three are
  read-mostly, idempotent, and skipped when retention is disabled.
  `CheckDirPerm` is gated by `runtime.GOOS != "windows"` (Windows
  reports synthetic perm bits).
- **Whisper warn-once for missing segments.** `whisper.Client` gains
  an optional `Logger` field (wired by `cmd/bot` and `cmd/mcp`). When
  a response decodes with no `segments[]`, the client emits a single
  `sync.Once`-guarded Warn per process lifetime so operators see
  that confidence detection is disabled (typical cause: whisper.cpp
  not returning `verbose_json`) without log spam.
- **Audit batch in CI.** Two new jobs in
  `.github/workflows/ci.yml` complement the existing `govulncheck`
  gate. Both fail the PR on any finding:
  - `semgrep` — `p/golang` + `p/security-audit` rulesets, pinned via
    `semgrep/semgrep:1.95.0` container, `--error` exit code. Two
    pre-existing false positives in `internal/db/notes.go` (the
    batch-IN `fmt.Sprintf(...placeholders)` pattern, where
    placeholders come from `len(ids)` and values bind through
    `ExecContext` args) are suppressed with inline
    `// nosemgrep:` comments + a short explanation.
  - `gitleaks` — `gitleaks/gitleaks-action@v2`, `fetch-depth: 0` so
    the scan sees every ancestor commit, not just `HEAD`.

  An `osv-scanner` job was tried and removed: for Go projects it
  produces non-actionable noise (every stdlib patch ships a fresh
  batch of OSV-indexed vulns; `osv-scanner` fails on all of them
  with no reachability filter). `govulncheck` already covers the
  same OSV database for Go with reachability analysis. A stub
  comment in `ci.yml` flags where to slot `osv-scanner` back if a
  non-Go transitive ecosystem ever appears in the tree.
- **Open-source readiness batch:** `Makefile` with `make test /
  test-race / build / vet / lint / vuln / fmt / tidy / ci / clean`
  mirroring CI; `.editorconfig` for tab/space consistency;
  `docs/ROADMAP.md` (next / mid-term / speculative / won't-do
  buckets); `docs/RELEASING.md` (pre-release checklist,
  tag-cutting steps, and how to reconcile the CHANGELOG-vs-git
  mismatch); a Documentation section in README that wires
  ARCHITECTURE / CONTRIBUTING / CHANGELOG / SECURITY /
  CODE_OF_CONDUCT / ROADMAP into the front page; codecov badge
  (codecov upload was already in CI, just the badge was missing).
- **Open-source documentation layer:** `CONTRIBUTING.md`,
  `ARCHITECTURE.md`, `CHANGELOG.md` itself,
  `.github/PULL_REQUEST_TEMPLATE.md`, `CODE_OF_CONDUCT.md`.
- **`schema_migrations` tracker** in `db.Migrate` —
  non-idempotent migrations (`ALTER ADD COLUMN`) run exactly once.
- **Opt-in pprof endpoint** (`internal/diag`). `PPROF_ADDR` env var,
  empty = disabled (production default). When set, both `cmd/bot` and
  `cmd/mcp` expose `net/http/pprof` on a dedicated `ServeMux`. The
  startup check refuses any non-loopback bind (only `127.0.0.0/8`,
  `[::1]`, `localhost` pass) — pprof leaks goroutine stacks and source
  lines, so the safe default is SSH-tunnel-to-loopback. Documented in
  `docs/CONFIG.md`.
- **MCP smoke harness** (`scripts/smoke-mcp.sh`). bash + curl + jq
  walk-through that asserts bearer-auth rejection (missing / wrong
  token → 401), `tools/list` completeness, and a happy-path response
  shape for every read-only tool against a *running* server — the
  integration gap the httptest-based unit tests can't cover (port
  binding, auth wrapper, env plumbing). Wired into `docs/RELEASING.md`
  as a pre-release manual gate. Mutating tools are opt-in via
  `--mutate` + `NOTE_ID`.

### Changed

- **`db_health` MCP tool gains a `quick` parameter.** Setting
  `quick=true` skips the slow full `PRAGMA integrity_check` (which
  can take >30s on a multi-GB DB) and runs only `quick_check` +
  count + size. The `integrity_check` field then returns the
  sentinel `"skipped"` so a caller can distinguish "not run" from a
  real corruption message. Tool timeout shrinks from 30s to 2s in
  quick mode. `db.Health(ctx)` became `db.Health(ctx, quickOnly
  bool)`.
- **MCP `NewServer` registers tools via a table.** The 9 inline
  `register*` calls are now a `[]registrar` slice — one line per
  tool, easier to scan than a constructor body. `retranscribe`
  stays outside the table because its signature includes
  `RetranscribeDeps`.
- **`composePrompt` extracted to `internal/promptbuilder`.** Both
  the bot's live-transcription path and the MCP `retranscribe` tool
  now call `promptbuilder.Compose(ctx, src, basePrompt, logger)`.
  Unifies two minor behavior differences (bot now `TrimSpace`s the
  basePrompt; MCP now Warn-logs `VocabPrompt` errors).
  `VocabSource` is a minimal interface — `*db.DB` satisfies it
  implicitly.
- **`mustEnv` + `HALLUCINATION_THRESHOLD` parsing extracted to
  `internal/config`.** `cmd/bot` and `cmd/mcp` no longer carry
  identical copies of `mustEnv` or the float-in-[0,1] env parser.
  `config.ParseFloat01` keeps the existing per-binary defaults
  intact.
- **`retranscribe` refuses discarded notes.** Previously the MCP
  tool would silently overwrite a discarded note's text. Now it
  returns an error hinting the caller to `restore_note` first.
  Mirrors the "discarded = forget this" signal the bot's UI already
  respects.
- **Telegram `c.Edit` failures are no longer swallowed.** The
  previous `_ = c.Edit(...)` callsites now route through
  `tb.tryEdit(...)`, which logs Warn on any error that's not
  Telegram's benign `"message is not modified"`. Real failures
  (network drop, message-too-old, invalid markup) now appear in the
  slog stream.
- **`/vocab` clear-cancel asymmetry documented** in
  `internal/telegram/vocab.go`. No code change — the empty `Data`
  payload on Yes/No is intentional (vocab has no stateful view to
  preserve across the confirm modal), but the asymmetry with
  `/pending`'s clear-cancel had to be called out so a future
  contributor doesn't "fix" it by mistake.
- **`migrations/` moved to `internal/db/migrations/`.** The
  migration runner (`db.Migrate`) lives in `internal/db`; the SQL
  files it embeds now sit next to it, following Go's "package owns
  its resources" convention. Embed pattern stayed `*.sql`
  (relative). Seven importers swapped `voicelog/migrations` →
  `voicelog/internal/db/migrations`. Top-level root no longer has
  a `migrations/` directory.
- **`internal/mcp/server.go` split by tool family.** 542 → 128
  lines in `server.go` (NewServer + shared helpers — `toMCP`,
  `jsonResult`, `toInt64Slice`, plus the `mcpNote` wire type). New
  same-package siblings: `tools_read.go` (the five read-only tools),
  `tools_mutate.go` (the three DB-writing tools that don't call
  out), `tools_retranscribe.go` (`RetranscribeDeps`,
  `retranscribeResponse`, and the registrar that pulls in whisper +
  audio + promptbuilder). Public API unchanged.
- **`internal/db/notes.go` split by domain.** The 562-line file
  became five focused siblings under the same package: `notes.go`
  (CRUD + `queryNotes` helper + types), `notes_search.go` (FTS5 +
  `NoteWithRank`), `notes_audio.go` (retention path helpers +
  dedup), `notes_history.go` (`ArchiveAndUpdateText`),
  `notes_health.go` (`Health` + `HealthReport`). All methods stayed
  on `*DB`; no caller change.
- **`internal/telegram/bot.go` split** into `bot.go` + `errors.go`
  + `saved_reply.go` + `list_view.go` + `vocab.go`. Buttons live
  next to their handlers, not in a central registry.
- **`telegram.New(...)` positional signature replaced by
  `telegram.Config`.** Add new knobs to `Config`, not to positional
  args.
- **`Bot.whisper` is a consumer-side `transcriber` interface.**
  `*whisper.Client` still satisfies the interface so `cmd/bot`
  passes the concrete type unchanged. Lets tests inject a fake
  without an HTTP server or ffmpeg.
- **`MCP search_notes` / `get_notes_in_range` exclude `discarded`
  notes by default.** Opt-in via `include_discarded=true` or
  explicit `status="discarded"`.
- **All raw `err.Error()` paths to the chat are now sanitized via
  `userErrMsg(label, err)`.** Raw errs go to `slog.Error` only —
  no more leaking internal hostnames or filesystem paths.
- **Dockerfiles relocated to `docker/`.** `Dockerfile.bot` and
  `Dockerfile.mcp` no longer sit at repo root.
  `docker-compose.yml`'s `dockerfile:` and
  `.github/workflows/ci.yml`'s `file:` updated to
  `docker/Dockerfile.{bot,mcp}`; build context stays at `.` so no
  COPY paths inside the Dockerfiles change.
- **README split into `docs/`.** Top-level `README.md` went from
  658 to 249 lines (overview + Why-this-exists + the
  differentiation table + Documentation index + Architecture +
  Quick start + Claude Code exposure + License/Acknowledgements).
  Everything else moved under `docs/`: `CONFIG.md`, `USAGE.md`,
  `MCP.md`, `SECURITY-MODEL.md`, `OPERATIONS.md`, `RUN-LOCALLY.md`,
  `ROADMAP.md`, `RELEASING.md`.

### Fixed

- **Dedup race closed via single-statement `INSERT WHERE NOT
  EXISTS`.** Pre-fix, two concurrent voice messages with identical
  bytes could each pass the bot's `FindRecentByHash` fast-lane
  check (no row yet) and both insert. `db.InsertNoteWithMeta` now
  ships an optional `DedupWindowSec` field on `NoteMeta`; when set
  alongside a non-empty `AudioHash`, the INSERT runs through a
  `WHERE NOT EXISTS` subquery against the same window. SQLite WAL
  serializes writers, so the second caller's subquery sees the
  first's row and zero rows are inserted. The caller gets the
  surviving row's id back alongside a new `db.ErrDuplicateAudio`
  sentinel; the bot surfaces the same "duplicate" reply the fast
  lane would have. Stale resends past the window still insert
  normally — the deliberate "user re-sends the same recording the
  next morning" semantic is preserved.
- **`MarkAnalyzed` non-idempotent under callback flood.** The
  WHERE clause was `status != 'discarded'`, which matched
  `analyzed → analyzed` no-op UPDATEs through modernc/sqlite's
  count-matched semantics. A 50× tap-storm on the same note
  reported 50 flips instead of 1 and re-wrote the same row 50
  times. Narrowed to `WHERE status = 'pending'` — semantically
  clearer ("only flip pending → analyzed") and properly idempotent.
- **F1 + F2 (MED → resolved):** `db.Migrate` now wraps the full
  apply loop in `BEGIN IMMEDIATE` on a dedicated `*sql.Conn`.
  Concurrent runners (bot + mcp on a fresh DB) serialize on
  SQLite's RESERVED lock; per-file failures roll back together
  with the `schema_migrations` row so re-runs are clean.
- **F3 (MED → resolved):** `notes.audio_path` is written as a
  basename relative to `AUDIO_DIR` for new rows; legacy absolute
  rows that point under the current `AUDIO_DIR` are normalized at
  startup by `audio.RelativizeLegacyPaths`. Read sites (janitor,
  MCP retranscribe) go through `audio.Resolve`, which still
  accepts both formats as a backward-compat fallback. New
  `RetranscribeDeps.AudioDir`; `cmd/mcp` picks up `AUDIO_DIR` (same
  default as the bot).
- **Startup Open race on fresh DB (LOW → resolved):** `db.Open`
  now retries `PingContext` with exponential backoff (~3.15s
  budget) on `SQLITE_BUSY`. Two concurrent processes hitting a
  fresh DB file used to collide on the `journal_mode=WAL` pragma
  write before `busy_timeout` was effective; first deploy of
  bot+mcp under docker-compose could restart-loop. Distinct from
  F1/F2 (which were about `Migrate`); same user-visible symptom.

### Security

- **Go toolchain bumped to clear stdlib CVEs at the source.** The
  `go` directive in `go.mod` went `1.25.5 → 1.26.1`, and CI's
  `setup-go` went `1.25 → 1.26` (test + govulncheck jobs), matching
  what the Docker images already build with (`golang:1.26-alpine`).
  This patches the batch of Go standard-library advisories surfaced
  by a one-off `osv-scanner` run (html/template, net/mail, net/url,
  net/http, crypto/tls, crypto/x509, archive/tar, archive/zip, os —
  ~20 CVEs). Most were already unreachable in this codebase
  (no `html/template`, `net/mail`, `httputil`, `archive/*`, or
  `os.Root` imports; `govulncheck`, the reachability-aware gate,
  stayed green), but bumping the toolchain removes them at the source
  instead of relying on reachability. `golang.org/x/sys` bumped to
  latest for CVE-2026-39824 (`NewNTUnicodeString`, Windows-only —
  never compiled into the Linux container, but cleared for
  completeness). `osv-scanner` remains out of CI (it re-flags every
  fresh Go release's stdlib advisories without a reachability filter);
  `govulncheck` + keeping the toolchain current is the standing
  posture.

### Tests

- **Coverage big batch.** internal/telegram from 35% → 55%,
  internal/mcp from 66% → 79%, internal/whisper from 67% → 84%.
  Highlights:
  - `internal/telegram/processfile_test.go` — 10 tests covering
    happy path (whisper → insert → saved-reply), EmptyTrans, dedup
    fast-lane hit, out-of-window resend falling through, NULL
    confidence on segment-less results, audio retention save +
    path-set, retention-off NULL audio_path,
    suspect_hallucination flagging, sanitized whisper-error reply
    (no hostname leak), typing notify.
  - `internal/telegram/callbacks_test.go` — 12 tests for the
    inline callback handlers (`cbDiscard`, `cbSavedRestore`,
    `cbSavedFull`, `cbVocab*`).
  - `internal/mcp/error_branches_test.go` — 21 tests for the
    second half of every tool handler: bad arg types, missing
    Required args, malformed RFC3339, `include_discarded` and
    explicit `status` overrides, retranscribe's no-audio guard,
    DB-closed propagation for read + mutate + db_health.
  - `internal/whisper/transcribe_outer_test.go` — 4 tests for the
    outer `Transcribe`. `whisper.Client` has an unexported `toWAV
    func(...) error` field that defaults to `ffmpegToWAV`; tests
    inject a stub so the converter doesn't need ffmpeg on PATH.
- **Fuzz corpus** for the parsers, FTS5 query, and
  `whisper.Aggregate`. Seven targets across three packages with
  hand-curated seed lists. All clean at 3s `-fuzztime` during
  development; CI can lengthen the budget without touching the
  code.
  - `internal/telegram/state_fuzz_test.go`:
    `FuzzParsePendingState`, `FuzzParseRecentState`,
    `FuzzParsePendingStateWithID`, `FuzzParseRecentStateWithID`,
    `FuzzValidDateKey`, `FuzzClampPage`, `FuzzValidRecentFilter`.
  - `internal/db/fuzz_test.go`: `FuzzSearchNotes_QueryString`,
    `FuzzAddVocab_Term`.
  - `internal/whisper/aggregate_fuzz_test.go`:
    `FuzzResult_Aggregate`.
- **`internal/mcp` integration tests** — 15 happy-path tests
  against a live `httptest.NewServer(mcp.BearerAuth(token,
  mcpHTTP))`. Cover bearer auth (missing / wrong / correct +
  WWW-Authenticate echo) plus every tool. As prep, `bearerAuth`
  moved from `cmd/mcp/main.go` to `internal/mcp/auth.go`
  (exported as `BearerAuth`) so the test exercises the same
  wrapper that ships in production.
- **`whisper.transcribeWAV` HTTP coverage** via
  `httptest.NewServer`: verbose_json happy path, plain-json
  fallback (no segments), prompt presence/absence in multipart,
  HTTP error propagation, malformed JSON body, missing wav file.
- **Goroutine lifecycle tests** for `audio.Janitor` and
  `db.MaintenanceLoop`. Janitor immediate-return when retention
  is disabled; both goroutines exit within 2s of ctx cancel even
  though their tickers are configured for hours/days.
- **Migration bootstrap test** — `TestMigrateBootstrap` in
  `internal/db/db_test.go` verifies that after a fresh `Migrate`
  call the DB has every table, column, FTS5 trigger, and index
  the codebase relies on, and that `schema_migrations` matches
  the list of `*.sql` files exactly. Catches "added a column in
  code, forgot the migration" regressions before the first
  INSERT.
- **`internal/diskguard` tests** — common test asserts
  `FreeBytes(t.TempDir())` returns `(non-zero, nil)` on every
  platform. Build-tagged tests cover the platform-specific
  surfaces. Removes the last `[no test files]` from
  `go test ./...`.
- **Callback flood stress test** — 50 concurrent UPDATEs on the
  same note (`TestMarkAnalyzed_CallbackFlood`,
  `TestDiscardNotes_CallbackFlood`) assert total flips == 1.
  Caught the `MarkAnalyzed` idempotency bug listed above.

## [0.1.0] — 2026-05-26

Initial public release.

- Telegram bot + MCP server + whisper.cpp HTTP, three Docker
  containers.
- SQLite + FTS5 storage (migration 001).
- 4 MCP tools: `list_pending_notes`, `get_notes_in_range`,
  `search_notes`, `mark_analyzed`.
- `/pending`, `/recent`, `/delete <id>` text commands.
- README + SECURITY policy + LICENSE (MIT).
