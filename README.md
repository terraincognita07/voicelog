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
                                       │   mark_analyzed
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

### 6. Expose the MCP server to Claude.ai

The `mcp` container binds only to `127.0.0.1:8081` on the host. Claude.ai's
remote MCP connector requires HTTPS — put a reverse proxy in front.

Example nginx config (assuming `voicelog.example.com` points at the server
and you have a Let's Encrypt cert via certbot):

```nginx
server {
    listen 443 ssl http2;
    server_name voicelog.example.com;

    ssl_certificate     /etc/letsencrypt/live/voicelog.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/voicelog.example.com/privkey.pem;

    location /mcp {
        proxy_pass http://127.0.0.1:8081;
        proxy_set_header Authorization $http_authorization;
        proxy_pass_header Authorization;
    }
}
```

In Claude.ai → Settings → Integrations → Add MCP server:

```
URL:   https://voicelog.example.com/mcp
Token: <your MCP_TOKEN>
```

Claude calls `get_notes_in_range`, reads the transcripts, replies in plain
language, then optionally calls `mark_analyzed` to mark the batch processed.

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
| `HOST_UID` | no | `1000` | UID of bot/mcp processes — must own `./data` on host |
| `HOST_GID` | no | `1000` | GID of bot/mcp processes — must own `./data` on host |

## MCP tools

Every request must carry `Authorization: Bearer $MCP_TOKEN`.

- **`list_pending_notes(limit?: int = 50)`** —
  last N notes with `status='pending'`, newest first
- **`get_notes_in_range(from: ISO8601, to: ISO8601, status?: string, limit?: int = 500)`** —
  date-window query, optional status filter (`pending|analyzed|discarded`).
  Hard cap 500 rows per response.
- **`search_notes(query: string, limit?: int = 20)`** —
  SQLite FTS5 MATCH. Supports bare words (AND), `"phrase"`, `term*`,
  `term1 OR term2`. Results sorted by bm25 rank (lower = better).
- **`mark_analyzed(ids: int[])`** —
  flip status to `analyzed` for the given ids. Discarded notes are
  not touched. Returns `{updated: N}`.

Tool schemas are visible via standard MCP `tools/list`.

## Telegram commands

All replies are in Russian — easy to localise in
`internal/telegram/bot.go`.

- `/pending` — last 20 pending notes (id, time, first 80 chars)
- `/recent` — last 10 notes regardless of status
- `/delete <id>` — mark a note as `discarded`
- `/help`, `/start` — show command list

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
