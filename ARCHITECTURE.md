# Architecture

voicelog is a small Go service split across three containers that share one
SQLite database. The architecture stays intentionally simple: no message
queue, no background workers beyond a few goroutines, no caches, no ORM.

## Containers

```
┌──────────────────────┐    ┌──────────────────────┐    ┌──────────────────────┐
│      whisper         │    │         bot          │    │         mcp          │
│  whisper.cpp server  │◀───┤  Telegram poller     │    │  HTTP MCP server     │
│  (OpenAI-compatible) │    │  ffmpeg → whisper    │    │  bearer-auth /mcp    │
│  internal, no auth   │    │  SQLite writer       │    │  SQLite reader+writer│
└──────────────────────┘    └──────────────────────┘    └──────────────────────┘
                                       │                          │
                                       └───── ./data/voicelog.db ─┘
                                                (SQLite + FTS5, WAL)
```

- **whisper** — `ggerganov/whisper.cpp` server image. CPU-only inference.
  No auth, internal compose network only.
- **bot** — long-polls Telegram, downloads voice messages, runs ffmpeg →
  whisper, persists transcriptions, owns audio retention copy step.
- **mcp** — HTTP server exposing 9 tools to Claude. Bearer-token auth.
  Runs the DB maintenance loop (weekly WAL checkpoint + monthly VACUUM).

## Code layout

```
voicelog/
├── cmd/
│   ├── bot/main.go          # wiring + env parsing
│   └── mcp/main.go          # wiring + env parsing + bearer auth
├── internal/
│   ├── audio/               # opt-in audio retention (SaveOriginal + Janitor)
│   ├── config/              # env parsing (MustEnv, ParseFloat01)
│   ├── db/                  # all SQL lives here
│   │   └── migrations/      # forward-only NNN_name.sql + embed.FS wrapper
│   ├── diag/                # gated pprof endpoint (PPROF_ADDR, loopback-only)
│   ├── diskguard/           # build-tagged free-space probe (unix / other)
│   ├── mcp/                 # MCP tool registrations
│   ├── promptbuilder/       # whisper prompt assembly (base prompt + vocab)
│   ├── telegram/            # bot handlers, locale, list views, vocab UI
│   └── whisper/             # HTTP client to whisper.cpp
└── docker/                  # Dockerfile.bot + Dockerfile.mcp
```

## Layering rules

Strict — enforced by review, not by build:

| Layer | What it does | Must NOT |
|---|---|---|
| `cmd/{bot,mcp}` | env parsing, dependency wiring | contain business logic |
| `internal/telegram` | Telegram updates, inline keyboards | reach into DB directly (always via `internal/db`) |
| `internal/mcp` | MCP tool registrations | implement business logic (delegate to db/whisper) |
| `internal/db` | all SQL, migrations, transactions | know about HTTP, Telegram, MCP |
| `internal/whisper` | HTTP client to whisper.cpp | parse user input |
| `internal/audio` | retain raw .oga, sweep old files | mutate `notes` table directly (delegate to db) |
| `internal/diskguard` | platform-specific syscall | depend on anything except `syscall` |
| `internal/config` | startup env parsing (`MustEnv`, `ParseFloat01`) | hold runtime state |
| `internal/promptbuilder` | assemble whisper prompt (base + vocab) | talk to Telegram / MCP |
| `internal/diag` | gated pprof listener (`PPROF_ADDR`, loopback-only) | run by default / bind non-loopback |

## Persistence

- SQLite + FTS5 + WAL mode.
- Schema lives in `internal/db/migrations/NNN_name.sql`, applied
  forward-only via `internal/db/db.go::Migrate`. A `schema_migrations`
  table tracks applied filenames so non-idempotent statements
  (`ALTER ADD COLUMN`) run exactly once.
- All queries are parameterized. FTS5 MATCH passes user input as a bind value.
- The `notes` table is the only mutable state worth backing up; `vocabulary`
  and `notes_history` are nice-to-have. `./data/voicelog.db` (+ `-wal` + `-shm`)
  is the entire backup unit.

## Telegram bot surface

Reading order:

1. `bot.go` — Bot struct + middleware + voice handling + simple `/start`,
   `/help`, `/delete`.
2. `errors.go` — sanitization layer (`userErrMsg`, `errReply`, `errToast`).
3. `saved_reply.go` — what the user sees after recording a voice (delete
   confirm, the `✏️`/`🗑` saved-reply markup, show-full).
4. `edit_note.go` — the `✏️ Edit` flow shared by the saved reply and the note
   card: in-place menu, replace-a-word (with an occurrence picker when the word
   matches more than once), rewrite-all, `📖 Show full` to expand a long note,
   and the in-memory `editState` slot.
5. `list_view.go` — `/pending` and `/recent`. State encoding, day grouping,
   filter chips, "Show more" pagination, mass-delete confirm. Each note's
   `[#id]` button opens its card.
6. `note_card.go` — the single-note card (edit / tag / delete) opened from a
   list, plus manual tag add/remove; `cardRef` callback encoding.
7. `vocab.go` — `/vocab` interactive editor with force-reply add prompt.
8. `locale.go` — every user-facing string in en + ru.

All inline-keyboard views thread their full state through callback `Data`
fields (capped at 64 bytes by Telegram). A unit test asserts the worst-case
encoding fits.

## MCP surface

Server: `internal/mcp/server.go` (constructor + helpers); tools live
in `tools_read.go` / `tools_mutate.go` / `tools_retranscribe.go` /
`tools_vocab.go` / `tools_tags.go`. Fifteen tools:

| Tool | Mutating? | Notes |
|---|---|---|
| `list_pending_notes` | no | basic CRUD |
| `get_notes_in_range` | no | date window + optional status filter |
| `search_notes` | no | FTS5 + bm25 + 30-token snippet; Cyrillic terms stemmed (Snowball ru) + prefix-matched |
| `get_note` | no | full raw_text |
| `mark_analyzed` | yes | batch |
| `delete_notes` | yes | batch, **permanent** — removes row + history + audio |
| `retranscribe` | yes | requires audio retention; archives to `notes_history` |
| `db_health` | no | `PRAGMA integrity_check` (+ optional `quick` mode) + counts |
| `list_vocab` | no | current whisper vocabulary terms |
| `add_vocab` | yes | batch add; Claude closes the transcription-quality loop |
| `remove_vocab` | yes | single term; `clear` stays bot-only by design |
| `tag_note` | yes | attach category tags (analysis-side overlay); normalized + deduped |
| `untag_note` | yes | remove one tag |
| `list_tags` | no | distinct tags + note counts, most-used first |
| `notes_by_tag` | no | deterministic selection by tag (complements `search_notes`) |

Every note object returned by a read tool carries its `tags` (batch-loaded
via `attachTags`, no N+1).

Every tool sets `ReadOnlyHint`, `DestructiveHint`, `IdempotentHint`,
`OpenWorldHint` explicitly.

## Background loops

- `audio.Janitor` (bot container, when `AUDIO_RETENTION_DAYS > 0`): every 6h
  deletes retained `.oga` files older than the window and nulls
  `notes.audio_path`. Safety rail: `pathInside` refuses paths that escape
  the managed dir.
- `db.MaintenanceLoop` (mcp container only, unless `DB_MAINTENANCE_DISABLED`):
  weekly `wal_checkpoint(TRUNCATE)`, monthly `VACUUM`.

## Security model

See `README.md` § "Security model" and `SECURITY.md`. Bearer token is the
only credential for MCP; `ALLOWED_USER_ID` is the only credential for the
bot. The threat-model boundary is **single-user self-host** — extending into
multi-tenant requires audit-log + rate-limit + token-per-tenant work that is
deliberately out of scope.

## Where to start as a contributor

- Bug in the bot UI → start at `internal/telegram/list_view.go` or
  `saved_reply.go`.
- Bug in an MCP tool → open the appropriate file under `internal/mcp/`:
  `tools_read.go` for read-only tools, `tools_mutate.go` for DB writers,
  `tools_retranscribe.go` for the whisper-dependent tool. Shared helpers
  live in `server.go`.
- Schema change → start with a new `internal/db/migrations/NNN_name.sql`,
  then add read/write helpers to `internal/db`.
- Whisper-side change → `internal/whisper/client.go` + `Result.Aggregate`.
- New tests → next to the file under test (`foo_test.go` beside `foo.go`).
