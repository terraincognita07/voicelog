# voicelog

Self-hosted Telegram voice journal with Claude analysis via MCP.

[![CI](https://github.com/terraincognita07/voicelog/actions/workflows/ci.yml/badge.svg)](https://github.com/terraincognita07/voicelog/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/terraincognita07/voicelog)](https://goreportcard.com/report/github.com/terraincognita07/voicelog)
[![Release](https://img.shields.io/github/v/release/terraincognita07/voicelog?display_name=tag)](https://github.com/terraincognita07/voicelog/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](https://github.com/terraincognita07/voicelog)
[![Self-hosted](https://img.shields.io/badge/Self--hosted-yes-2ea44f)](https://github.com/terraincognita07/voicelog)

Send voice messages to your private Telegram bot. They are
transcribed locally with [whisper.cpp](https://github.com/ggml-org/whisper.cpp)
and stored in SQLite. Then in any Claude conversation, ask
*"what was I worrying about last week?"* — Claude reads the corpus via
an MCP server you self-host.

```
    you 🎙 voice ──→ | Telegram  |
                     |   bot     | — ffmpeg → whisper.cpp — text  ─┐
                                                                   │
                     | INSERT                                      │
                     └──────────→ | SQLite + FTS5    |  ←──────────┘
                                  | ./data/voicelog.db |
                                       │
                                       │   tools:
                                       │   list_pending_notes
                                       │   get_notes_in_range
                                       │   search_notes
                                       │   get_note
                                       │   mark_analyzed
                                       │   discard_notes
                                       │   restore_note
                                       │   retranscribe
                                       ▼
    Claude.ai ← MCP over HTTPS ── nginx ── | voicelog-mcp  |
```

## Why this exists

Most Telegram-whisper bots are stateless: send voice, get text, repeat.
Some bundle in LLM summarization. None build a searchable personal
corpus and let Claude analyze it on demand.

voicelog takes the opposite posture:

- The bot has **zero LLM logic** — it just captures.
- All reasoning happens externally in Claude, against your corpus, via MCP.

If you don't use Claude (or another MCP client) this isn't for you.
If you do, it lets you talk to your past self.

### How it differs from similar projects

| Project | Pattern | Persistence | Self-hosted |
|---|---|---|---|
| [whisper-telegram-mcp](https://glama.ai/mcp/servers/abid-mahdi/whisper-telegram-mcp) | MCP-as-tool (Claude triggers transcription) | No | Yes |
| [antirez/whisperbot](https://github.com/antirez/whisperbot) | Transcribe-and-reply | No | Yes |
| [Voicenotes.com](https://voicenotes.com) | MCP-as-corpus | Yes (cloud) | No, paid SaaS |
| **voicelog** | **MCP-as-corpus** | **Yes, local SQLite + FTS5** | **Yes** |

## Architecture

Three containers sharing one SQLite file:

| Service | Role | Image | RAM |
|---|---|---|---|
| `whisper` | `whisper.cpp` server, CPU-only inference | `ghcr.io/ggml-org/whisper.cpp:main` | ~800 MB (small model) |
| `bot` | Telegram poller → ffmpeg → whisper HTTP → SQLite INSERT | alpine + ffmpeg | ~30 MB |
| `mcp` | HTTP server exposing 4 tools to Claude over MCP | distroless static | ~20 MB |

Pure Go (`modernc.org/sqlite`, `CGO_ENABLED=0`). No native deps to audit.

## Quick start

### Prerequisites

- Linux server with **Docker** + **Docker Compose v2**
- ~1 GB free RAM (for `small-q5_1` model) or ~2 GB (for `medium-q5_0`)
- ~2 GB disk: images (~700 MB), model (~190 MB), DB grows slowly
- A domain with HTTPS — needed only if you want Claude.ai to reach the MCP

### 1. Telegram setup

Talk to [@BotFather](https://t.me/BotFather) in Telegram:

1. `/newbot` → name your bot → **save the BOT_TOKEN it gives you**
2. `/setprivacy` → choose your bot → `Disable`
   (without this, in group chats the bot only sees commands; for DMs it
   doesn't matter, but disabling is safer if you ever forward into a group)

Find your numeric user ID by messaging [@userinfobot](https://t.me/userinfobot)
— it replies with your `id` (e.g. `123456789`). The bot will refuse messages
from any other user.

### 2. Clone and configure

```bash
git clone https://github.com/terraincognita07/voicelog.git
cd voicelog
cp .env.example .env
```

Generate a strong MCP token:

```bash
openssl rand -hex 32
```

Open `.env` and fill in:

```dotenv
BOT_TOKEN=8123456789:AAA...            # from BotFather
ALLOWED_USER_ID=123456789              # from @userinfobot
MCP_TOKEN=<paste output of openssl above>
TZ=Europe/Moscow                       # your timezone
HOST_UID=1000                          # `id -u` on your server
HOST_GID=1000                          # `id -g` on your server
```

Check your UID/GID — if not `1000`, update the file:

```bash
id -u && id -g
```

### 3. Download the whisper model

```bash
chmod +x scripts/*.sh
scripts/fetch-model.sh                # ggml-small-q5_1.bin, ~190 MB
```

Other options:
```
scripts/fetch-model.sh ggml-small-q8_0.bin     # +75 MB, marginal quality bump
scripts/fetch-model.sh ggml-medium-q5_0.bin    # 314 MB, needs ~1.7 GB RAM
```

If you change the model, update the `-m` flag in `docker-compose.yml`:

```yaml
command:
  - "whisper-server --host 0.0.0.0 --port 8080 -m /models/<your-model>.bin -t 4 -l auto"
```

### 4. Prepare host directories

The `bot` and `mcp` containers write to `./data` as your UID/GID:

```bash
mkdir -p data
chown -R $(id -u):$(id -g) data
```

### 5. Build and run

```bash
docker compose build          # ~2–3 min first time
docker compose up -d
docker compose logs -f bot mcp
```

Send a voice message to your bot in Telegram. Within a few seconds:

```
voicelog-bot | {"level":"INFO","msg":"transcribing", ...}
```

And the bot replies:

```
✓ записано #1 (1 pending)
```

Verify with `/pending` in Telegram, or:

```bash
docker compose exec bot ls -la /data      # voicelog.db, .db-wal, .db-shm
```

### 6. Expose the MCP server to a Claude client

The mcp container binds only to `127.0.0.1:8081`. How to expose it depends
on which Claude client you use.

#### 6a. Claude Code (CLI on your laptop) — no domain needed

Open an SSH tunnel from the server to your laptop:

```bash
ssh -L 8081:127.0.0.1:8081 user@your-server
```

Or as a permanent entry in `~/.ssh/config`:

```
Host myserver
  HostName your-server
  User user
  LocalForward 8081 127.0.0.1:8081
```

Then in Claude Code MCP config (`~/.claude/mcp.json` or via `claude mcp add`):

```json
{
  "mcpServers": {
    "voicelog": {
      "url": "http://127.0.0.1:8081/mcp",
      "headers": { "Authorization": "Bearer YOUR_MCP_TOKEN" }
    }
  }
}
```

Done — no public DNS, no TLS, no reverse proxy.

#### 6b. Claude.ai (web) — needs public HTTPS

Claude.ai's *Add custom connector* dialog currently exposes **only OAuth
fields** — there is no UI to set a custom `Authorization` header. To use
it with our bearer-token server, put the token into the URL itself and
let the reverse proxy inject the header before it reaches mcp.

The configs below expose two routes side by side:

| Route | Auth | Use case |
|---|---|---|
| `/mcp` | `Authorization: Bearer <token>` header | Claude Code, programmatic clients, monitoring |
| `/t/<token>/mcp` | none (token is in URL) | Claude.ai web |

Pick **nginx** or **Traefik**. For Traefik, pick docker-labels or file-provider
depending on how your Traefik is configured.

##### nginx

```nginx
# Substitute YOUR_MCP_TOKEN and voicelog.example.com below.
server {
    listen 443 ssl http2;
    server_name voicelog.example.com;

    ssl_certificate     /etc/letsencrypt/live/voicelog.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/voicelog.example.com/privkey.pem;

    # Header-based route — Bearer required from the client.
    location = /mcp {
        proxy_pass http://127.0.0.1:8081;
        proxy_http_version 1.1;
        proxy_buffering off;
        proxy_read_timeout 600s;
        proxy_set_header Authorization $http_authorization;
    }

    # Path-based route — token in URL, nginx injects the header.
    location = /t/YOUR_MCP_TOKEN/mcp {
        proxy_pass http://127.0.0.1:8081/mcp;
        proxy_http_version 1.1;
        proxy_buffering off;
        proxy_read_timeout 600s;
        proxy_set_header Authorization "Bearer YOUR_MCP_TOKEN";
    }
}
```

##### Traefik — docker-provider (labels)

In `docker-compose.override.yml`:

```yaml
services:
  mcp:
    ports: !reset []
    networks:
      - default
      - traefik          # your Traefik docker network
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=traefik"

      # Router 1: /mcp with Bearer
      - "traefik.http.routers.voicelog-mcp.rule=Host(`voicelog.example.com`) && Path(`/mcp`)"
      - "traefik.http.routers.voicelog-mcp.entrypoints=websecure"
      - "traefik.http.routers.voicelog-mcp.tls.certresolver=letsencrypt"
      - "traefik.http.routers.voicelog-mcp.service=voicelog-mcp"

      # Router 2: /t/<token>/mcp without header
      - "traefik.http.routers.voicelog-mcp-path.rule=Host(`voicelog.example.com`) && PathPrefix(`/t/YOUR_MCP_TOKEN/mcp`)"
      - "traefik.http.routers.voicelog-mcp-path.entrypoints=websecure"
      - "traefik.http.routers.voicelog-mcp-path.tls.certresolver=letsencrypt"
      - "traefik.http.routers.voicelog-mcp-path.middlewares=voicelog-stripprefix,voicelog-addauth"
      - "traefik.http.routers.voicelog-mcp-path.service=voicelog-mcp"

      - "traefik.http.middlewares.voicelog-stripprefix.stripprefix.prefixes=/t/YOUR_MCP_TOKEN"
      - "traefik.http.middlewares.voicelog-addauth.headers.customrequestheaders.Authorization=Bearer YOUR_MCP_TOKEN"

      - "traefik.http.services.voicelog-mcp.loadbalancer.server.port=8081"

networks:
  traefik:
    external: true
    name: traefik
```

##### Traefik — file-provider (`dynamic/voicelog.yml`)

```yaml
http:
  routers:
    voicelog-mcp:
      rule: "Host(`voicelog.example.com`) && Path(`/mcp`)"
      entryPoints: [websecure]
      tls:
        certResolver: letsencrypt
      service: voicelog-mcp

    voicelog-mcp-path:
      rule: "Host(`voicelog.example.com`) && PathPrefix(`/t/YOUR_MCP_TOKEN/mcp`)"
      entryPoints: [websecure]
      tls:
        certResolver: letsencrypt
      middlewares: [voicelog-stripprefix, voicelog-addauth]
      service: voicelog-mcp

  middlewares:
    voicelog-stripprefix:
      stripPrefix:
        prefixes: ["/t/YOUR_MCP_TOKEN"]
    voicelog-addauth:
      headers:
        customRequestHeaders:
          Authorization: "Bearer YOUR_MCP_TOKEN"

  services:
    voicelog-mcp:
      loadBalancer:
        servers:
          - url: "http://voicelog-mcp:8081"
```

Smoke-test both routes from outside the server:

```bash
# Header-based
curl -s -X POST https://voicelog.example.com/mcp \
  -H "Authorization: Bearer YOUR_MCP_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | jq '.result.tools[].name'

# Path-based
curl -s -X POST https://voicelog.example.com/t/YOUR_MCP_TOKEN/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | jq '.result.tools[].name'
```

Both must return the four tool names.

##### Register in Claude.ai

Settings → Connectors → **Add custom connector**:

- **Name:** `voicelog`
- **Remote MCP server URL:** `https://voicelog.example.com/t/YOUR_MCP_TOKEN/mcp`
- **OAuth fields:** leave blank

Open a new chat — Claude sees four `voicelog__*` tools.

#### 6c. Tradeoffs of path-based auth

Putting the token into the URL means it appears in:

- the reverse-proxy access log
- browser history if you ever paste the URL into a browser
- `docker inspect` for the proxy (labels mode) or the dynamic YAML (file mode)

For personal self-host these are usually acceptable. To mitigate:

- Prefer Claude Code over Claude.ai web (scenario 6a — no token in URL)
- Rotate the token periodically: `openssl rand -hex 32`, update `.env`
  **and** the proxy config, then `docker compose up -d mcp` + reload the
  proxy

## Configuration reference

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `BOT_TOKEN` | yes | — | Telegram bot token from BotFather |
| `ALLOWED_USER_ID` | yes | — | The only Telegram user ID allowed to talk to the bot |
| `DB_PATH` | yes | — | SQLite file path inside the container, e.g. `/data/voicelog.db` |
| `WHISPER_URL` | yes | — | Whisper `/inference` endpoint, e.g. `http://whisper:8080/inference` |
| `MCP_TOKEN` | yes | — | Bearer token for the MCP server. Min 16 chars. Use `openssl rand -hex 32`. |
| `MCP_PORT` | no | `8081` | Port the mcp container listens on inside the host |
| `TZ` | no | UTC | Timezone for log timestamps and bot replies |
| `BOT_LOCALE` | no | `en` | Bot reply language: `en` or `ru`. Commands are unchanged in any locale. |
| `WHISPER_PROMPT` | no | — | Optional whisper "initial prompt" (admin default). User-managed vocabulary (`/vocab`) is appended after this. |
| `AUDIO_RETENTION_DAYS` | no | `0` | If `> 0`, keep the original `.oga` voice file at `AUDIO_DIR/<note_id>.oga` for that many days. A background janitor sweeps every 6h. `0` (default) disables retention — audio is deleted right after transcription. |
| `AUDIO_DIR` | no | `/data/audio` | Where retained `.oga` files live. Only consulted when `AUDIO_RETENTION_DAYS > 0`. |
| `HALLUCINATION_THRESHOLD` | no | `0.6` | Whisper hallucination detector cutoff (float ∈ [0, 1]). First segment's `no_speech_prob` above this flags the note as suspect. Raise to be stricter, lower to be looser. |
| `HOST_UID` | no | `1000` | UID of bot/mcp processes — must own `./data` on host |
| `HOST_GID` | no | `1000` | GID of bot/mcp processes — must own `./data` on host |

## MCP tools

Every request must be authenticated — either via `Authorization: Bearer $MCP_TOKEN`
on the `/mcp` route, or via the token-in-URL pattern on `/t/<token>/mcp`
(see section 6b).

- **`list_pending_notes(limit?: int = 50)`** —
  last N notes with `status='pending'`, newest first
- **`get_notes_in_range(from: ISO8601, to: ISO8601, status?: string, include_discarded?: bool = false, limit?: int = 500)`** —
  date-window query, optional status filter (`pending|analyzed|discarded`).
  Discarded notes are excluded by default; pass `include_discarded=true` or
  `status="discarded"` to surface them. Hard cap 500 rows per response.
- **`search_notes(query: string, limit?: int = 20, include_discarded?: bool = false)`** —
  SQLite FTS5 MATCH. Supports bare words (AND), `"phrase"`, `term*`,
  `term1 OR term2`. Results sorted by bm25 rank (lower = better). Each hit
  includes a `snippet` field — ~30 tokens around the match with the
  matched term wrapped in `<<` / `>>` and elided context shown as `...`.
  Discarded notes are filtered out by default; opt in via `include_discarded`.
- **`get_note(id: int)`** —
  fetch one full note by id. Returns the note object or an error if the
  id is unknown. Every note returned by MCP carries `confidence_overall`,
  `confidence_min` (mean / worst whisper `avg_logprob` — closer to 0 is
  more confident; `null` if the note was created before the verbose-JSON
  pipeline), and `suspect_hallucination` (bool — first whisper segment
  exceeded the silence-probability threshold).
- **`mark_analyzed(ids: int[])`** —
  flip status to `analyzed` for the given ids. Discarded notes are
  not touched. Returns `{updated: N}`.
- **`discard_notes(ids: int[])`** —
  mark the given ids as discarded (batch parity with the bot's `/delete`).
  Already-discarded rows are ignored. Returns `{updated: N}`. Reversible
  via `restore_note`.
- **`restore_note(id: int)`** —
  flip a single discarded note back to `pending`. Returns
  `{restored: bool}` — `true` if it was discarded and got restored,
  `false` if it exists but was not in `discarded` state.
- **`retranscribe(id: int)`** —
  re-run whisper on the note's retained audio file (requires
  `AUDIO_RETENTION_DAYS > 0` on the bot side, AND the note's audio still
  on disk). The previous `raw_text` is archived in `notes_history`
  before the row is updated, so the change is reversible at the SQL
  level. Returns `{note_id, old_text, new_text, confidence_overall,
  confidence_min, suspect_hallucination}` so the caller can summarize
  the diff. Requires the mcp container to be wired with `WHISPER_URL`
  (see `docker-compose.yml`).

Tool schemas are visible via standard MCP `tools/list`.

## Telegram commands

Reply language is selected via `BOT_LOCALE` (`en` default, `ru` opt-in);
commands themselves are not translated. Add more locales by appending to
the `locales` map in `internal/telegram/locale.go`.

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

### UI

- **Persistent menu** at the bottom of the chat: `Pending` / `Recent` /
  `Vocab` / `Help`. One-tap shortcut to the equivalent command.
- **Synced command menu** (blue button next to the input) — populated on
  startup via `setMyCommands`; no BotFather configuration needed.
- **Inline 🗑 button** under every saved-note reply — one tap marks the
  just-recorded note as `discarded` without typing `/delete <id>`.
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
  (3)` / `📅 Пн, 26 мая (3)` — easier to skim than bare ISO dates.

The whisper "initial prompt" sent with each transcription is composed as
`WHISPER_PROMPT` (env, admin-default) followed by the `/vocab` terms.

## Development

Requires Go 1.25+.

```bash
git clone https://github.com/terraincognita07/voicelog.git
cd voicelog
go mod download
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go test ./...
```

Project layout:

```
cmd/
  bot/main.go          # Telegram poller, transcription pipeline
  mcp/main.go          # HTTP MCP server, bearer auth
internal/
  whisper/             # HTTP client + ffmpeg wrapper
  telegram/            # bot handlers and user gate
  mcp/                 # tool registrations
migrations/
  001_init.sql         # schema with FTS5 triggers
  migrations.go        # embed.FS
scripts/
  fetch-model.sh       # download a whisper ggml model
  whisper-smoke.sh     # local smoke test against whisper-server
Dockerfile.bot         # alpine + ffmpeg, USER 10000
Dockerfile.mcp         # distroless/static, USER nonroot
docker-compose.yml     # 3 services, shared ./data volume
.env.example           # all configuration documented
```

## Security model

- **Single-user.** The bot ignores every Telegram user except `ALLOWED_USER_ID`.
  Rejected messages are dropped silently — the bot does not advertise its
  existence to strangers.
- **MCP bearer token.** Every MCP request must carry
  `Authorization: Bearer $MCP_TOKEN`. Comparison is constant-time
  (`crypto/subtle.ConstantTimeCompare`). Mismatch → `401`.
- **Localhost binding.** The mcp container binds to `127.0.0.1:8081` on the
  host. Internet exposure is your reverse-proxy's job — terminate TLS there,
  do not change the binding to `0.0.0.0`.
- **Resource caps.**
  - `get_notes_in_range` is capped at 500 rows per response
  - `search_notes` has a 3-second hard timeout
  - `list_pending_notes` / `mark_analyzed` have 2-second timeouts
- **No telemetry.** Nothing leaves your server except (a) Telegram API traffic
  and (b) MCP responses to Claude.ai when you trigger them.
- **CGO disabled.** Pure-Go SQLite via `modernc.org/sqlite`. No native deps.
- **Sanitized errors.** The bot never sends raw `err.Error()` to chat — that
  used to leak internal hostnames (`whisper`), filesystem paths
  (`/data/voicelog.db`), and third-party HTTP body content. Users see e.g.
  `⚠ Speech recognition unavailable. Try again in a moment.`; the full err
  stays in `slog`.
- **Throttled rejection logs.** Anonymous users get a single Warn line per
  user-id per 15 minutes (not per-message) — protects the log from
  someone spamming the bot to fill disk.
- **Defensive callback parsing.** Every inline-button `Data` field is
  parsed through whitelist validators (status enum, date format, integer
  range). Crafted callback data cannot smuggle arbitrary strings into
  view state.

### What's NOT protected — read this if you self-host

voicelog ships secured for "single-user self-hosted, personal-use". It is
**not** ready for shared / multi-tenant / regulated-health-data
deployment. Specifically:

- **No MCP rate-limit.** A leaked `MCP_TOKEN` gives full read+mutate access
  with no per-IP / per-second throttle. Rotate aggressively if you screen-
  share, paste it into a chat, or commit a config snippet by accident.
- **No audit log.** `get_note`, `search_notes`, `discard_notes` etc. don't
  record who accessed what. If you need "who read note #42 in the last
  30 days?" — there's no answer to give. Fine for personal use, not for
  compliance.
- **Audio in tmp.** During transcription each clip lives ~10 seconds at
  `/tmp/voicelog-*/src.wav`. A co-tenant with shell access to the container
  could read it. No encryption-at-rest.
- **Audio retention is plaintext on disk.** When `AUDIO_RETENTION_DAYS > 0`,
  the bot persists each voice's original `.oga` at `AUDIO_DIR/<note_id>.oga`
  with file mode `0600`. No encryption — host-disk encryption is your
  responsibility. The janitor sweeps every 6h; the cleanup window is your
  retention setting, not the original record date.
- **Whisper prompt is user-controlled.** `/vocab add` lets you write any
  string into the prompt sent to whisper.cpp. whisper.cpp is a transcription
  model, not an instruction-follower, so prompt-injection has no real
  blast radius today. If you swap whisper for an instruction-tuned ASR,
  reassess.
- **No public-API hardening on the MCP host.** The bearer-only model assumes
  the MCP endpoint sits behind your reverse proxy with TLS and ideally
  IP-allowlisted. If you publish `/t/<token>/mcp` to the open internet
  without a WAF, token brute-force is bounded only by Telegram-MCP request
  rate, not anything voicelog does.
- **TZ-dependent grouping.** Day grouping (`📅 today`/`yesterday`/dates)
  uses the bot container's `$TZ`. If you change `$TZ` mid-day, "today"
  shifts. Not a security issue — surfaces a UX subtlety worth knowing.
- **Bot token in env var.** Standard practice; readable by any process
  inside the bot container or anyone with `docker compose config`. If you
  need stronger isolation, use Docker secrets.

## Operational notes

- **Backups.** `./data/voicelog.db` is everything. SQLite WAL mode is on; copy
  with `sqlite3 data/voicelog.db ".backup '/backup/voicelog-$(date +%F).db'"`.
- **Model swap.** Stop compose, update `-m` flag in `docker-compose.yml`,
  fetch the new model, restart. No DB migration needed.
- **Disk growth.** Transcripts are text. 1000 voice notes ≈ 1 MB.
  No audio is persisted — temp WAVs are deleted after transcription.
- **Bot doesn't reply.** Check `docker compose logs bot`. Most common cause:
  wrong `ALLOWED_USER_ID`, or rejected by Telegram (invalid token).
- **MCP 502 from nginx.** Container probably not running — `docker compose ps`.

## Roadmap (out of scope for v1)

- Multi-user with per-user DB scoping
- Audio retention with `audio_path` populated for re-transcription
- Embeddings / vector search alongside FTS5
- Web UI for browsing the corpus

## License

MIT — see [LICENSE](LICENSE).

## Acknowledgements

- [whisper.cpp](https://github.com/ggml-org/whisper.cpp) — Georgi Gerganov
- [mcp-go](https://github.com/mark3labs/mcp-go) — mark3labs
- [telebot.v3](https://github.com/tucnak/telebot) — Tucnak
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — Jan Mercl
