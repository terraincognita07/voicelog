# voicelog

Self-hosted Telegram voice journal with Claude analysis via MCP.

[![CI](https://github.com/terraincognita07/voicelog/actions/workflows/ci.yml/badge.svg)](https://github.com/terraincognita07/voicelog/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/terraincognita07/voicelog/branch/main/graph/badge.svg)](https://codecov.io/gh/terraincognita07/voicelog)
[![Go Report Card](https://goreportcard.com/badge/github.com/terraincognita07/voicelog)](https://goreportcard.com/report/github.com/terraincognita07/voicelog)
[![Release](https://img.shields.io/github/v/release/terraincognita07/voicelog?display_name=tag)](https://github.com/terraincognita07/voicelog/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](https://github.com/terraincognita07/voicelog)
[![Self-hosted](https://img.shields.io/badge/Self--hosted-yes-2ea44f)](https://github.com/terraincognita07/voicelog)

Send voice messages (or plain text) to your private Telegram bot. Voice is
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
                                       ▼
    Claude.ai ← MCP over HTTPS ── nginx ── | voicelog-mcp  |
```

## Features

- **Voice + text capture** — speak on the go or type when you can't; both
  become searchable, taggable, editable notes.
- **Local transcription** — whisper.cpp on your own box, nothing leaves it.
- **Query via Claude (MCP)** — search, summarize and review your corpus on
  demand in plain language; 15 MCP tools, with example prompts in
  [docs/MCP.md](docs/MCP.md#asking-claude-example-prompts).
- **Tags** — label notes with categories that aren't in the words
  (`#идея`, `#философия`) and pull them back deterministically with
  `notes_by_tag`. Claude tags via MCP; you add/remove them by hand from a
  note's card. Shown `🏷` inline in the bot's lists.
- **Russian-aware search** — Cyrillic queries are stemmed (Snowball) so
  searching `работа` also finds `работе`/`работу`; Latin terms stay exact.
- **Fix what whisper misheard** — `✏️ Edit` any note right in Telegram: a
  button menu walks you through replacing one word or rewriting the whole note
  (previous version archived), or have Claude `retranscribe` it.
- **Self-improving accuracy** — Claude can spot recurring names/jargon across
  the corpus and add them to the whisper vocabulary via MCP (`add_vocab`).
- **Button-driven UI** — pending queue, day-grouped lists, status filters,
  per-note cards (edit / tag / delete) — all inline, typed commands are a
  fallback.
- **Single-user, self-hosted** — Telegram allow-list + bearer-token MCP;
  pure-Go (`CGO_ENABLED=0`), three small containers.

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

## Documentation

| | |
|---|---|
| [docs/CONFIG.md](docs/CONFIG.md) | Every env var, defaults, rotation guidance |
| [docs/USAGE.md](docs/USAGE.md) | Telegram commands and inline UI |
| [docs/MCP.md](docs/MCP.md) | MCP tool reference + Claude.ai web exposure (nginx / Traefik) |
| [docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md) | Threat model and what's NOT protected |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Backups, model swap, DB maintenance, debugging |
| [docs/RUN-LOCALLY.md](docs/RUN-LOCALLY.md) | Local dev loop, project layout, Makefile targets |
| [docs/ROADMAP.md](docs/ROADMAP.md) | What's next / mid-term / speculative |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Internal layering rules |
| [CONTRIBUTING.md](CONTRIBUTING.md) | PR setup and conventions |
| [CHANGELOG.md](CHANGELOG.md) | What shipped per release |
| [SECURITY.md](SECURITY.md) | Vulnerability disclosure |
| [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) | Community ground rules |

## Architecture

Three containers sharing one SQLite file:

| Service | Role | Image | RAM |
|---|---|---|---|
| `whisper` | `whisper.cpp` server, CPU-only inference | `ghcr.io/ggml-org/whisper.cpp:main` | ~800 MB (small model) |
| `bot` | Telegram poller → ffmpeg → whisper HTTP → SQLite INSERT | alpine + ffmpeg | ~30 MB |
| `mcp` | HTTP server exposing tools to Claude over MCP | distroless static | ~20 MB |

Pure Go (`modernc.org/sqlite`, `CGO_ENABLED=0`). No native deps to audit.
For full layering rules see [ARCHITECTURE.md](ARCHITECTURE.md).

## Quick start

### Prerequisites

- Linux server with **Docker** + **Docker Compose v2**
- ~1 GB free RAM (for `small-q5_1` model) or ~2 GB (for `medium-q5_0`)
- ~2 GB disk: images (~700 MB), model (~190 MB), DB grows slowly
- A domain with HTTPS — needed only if you want Claude.ai web to reach
  the MCP. Claude Code (CLI) works over an SSH tunnel without any of that.

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

The full env-var reference (defaults, optional knobs, rotation) is in
[docs/CONFIG.md](docs/CONFIG.md).

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

> **Version stamping (optional).** The mcp container reports a version to
> Claude in the MCP handshake. Built as above it reports `dev`. To stamp
> the real release tag instead, prefix the build with `VERSION` derived
> from git (run on a checkout that has the tag):
>
> ```bash
> VERSION=$(git describe --tags --dirty | sed 's/^v//') docker compose up -d --build
> ```
>
> Plain `docker compose up -d` keeps working — `dev` is just the honest
> answer for an unstamped build.

Send a voice message to your bot in Telegram. Within a few seconds:

```
voicelog-bot | {"level":"INFO","msg":"transcribing", ...}
```

And the bot replies:

```
✓ Note #1 saved · 0:05 · 1 pending
```

Verify with `/pending` in Telegram, or:

```bash
docker compose exec bot ls -la /data      # voicelog.db, .db-wal, .db-shm
```

### 6. Expose the MCP server to Claude Code

The mcp container binds only to `127.0.0.1:8081`. The simplest exposure
— and the one with no token-in-URL tradeoff — is an SSH tunnel from
your laptop:

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

For **Claude.ai web** (needs a public HTTPS endpoint, nginx / Traefik
config + token-in-URL workaround) see [docs/MCP.md](docs/MCP.md).

## Roadmap

See [docs/ROADMAP.md](docs/ROADMAP.md) for the candidate list (next /
mid-term / speculative / won't-do). Open a discussion before starting
work on a roadmap item so we don't overlap.

## License

MIT — see [LICENSE](LICENSE).

## Acknowledgements

- [whisper.cpp](https://github.com/ggml-org/whisper.cpp) — Georgi Gerganov
- [mcp-go](https://github.com/mark3labs/mcp-go) — mark3labs
- [telebot.v3](https://github.com/tucnak/telebot) — Tucnak
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) — Jan Mercl
- [snowball](https://github.com/kljensen/snowball) — Kyle Jensen (Russian stemming for search)
