# Running voicelog locally (for development)

Requires Go 1.26+.

```bash
git clone https://github.com/terraincognita07/voicelog.git
cd voicelog
go mod download
CGO_ENABLED=0 go build ./...
make test
```

`make ci` runs the same gates GitHub Actions runs — vet, staticcheck,
build, race-tests with coverage, govulncheck.

## Project layout

```
cmd/
  bot/main.go            # Telegram poller, transcription pipeline
  mcp/main.go            # HTTP MCP server, bearer auth
internal/
  whisper/               # HTTP client + ffmpeg wrapper
  telegram/              # bot handlers and user gate
  mcp/                   # MCP tool registrations + BearerAuth
  db/                    # SQLite + FTS5 + migrations runner
    migrations/          # forward-only NNN_name.sql + embed.FS
  audio/                 # opt-in retention (SaveOriginal + Janitor + Resolve)
  config/                # MustEnv + ParseFloat01 for cmd/*
  diskguard/             # build-tagged free-space probe (unix/other)
  diag/                  # gated loopback-only pprof endpoint
  promptbuilder/         # composes whisper "initial prompt" from base + vocab
scripts/
  fetch-model.sh         # download a whisper ggml model
  whisper-smoke.sh       # local smoke test against whisper-server
  smoke-mcp.sh           # MCP release-gate smoke test (see RELEASING.md)
docker/
  Dockerfile.bot         # alpine + ffmpeg, USER 10000
  Dockerfile.mcp         # distroless/static, USER nonroot
docker-compose.yml       # 3 services, shared ./data volume
.env.example             # all configuration documented
```

See [ARCHITECTURE.md](../ARCHITECTURE.md) for the layering rules each
of those directories follows.

## Useful Makefile targets

| Target | Purpose |
|---|---|
| `make test` | Test suite, no race detector, fast loop |
| `make test-race` | Test suite with race detector + coverage profile |
| `make build` | `CGO_ENABLED=0 go build ./...` (matches production images) |
| `make vet` / `make lint` | `go vet` / `staticcheck` |
| `make vuln` | `govulncheck ./...` |
| `make fmt` / `make tidy` | `go fmt` / `go mod tidy` |
| `make ci` | Run the lot — what CI runs on push/PR |

## Running without Docker

For tight inner-loop work you can run the bot and MCP binaries directly
against a host-installed whisper.cpp:

```bash
# Whisper server (in another terminal)
./build/bin/whisper-server -m models/ggml-small-q5_1.bin --host 127.0.0.1 --port 8080

# Bot
BOT_TOKEN=... ALLOWED_USER_ID=... DB_PATH=./voicelog.db \
WHISPER_URL=http://127.0.0.1:8080/inference \
go run ./cmd/bot

# MCP (in another terminal)
DB_PATH=./voicelog.db MCP_TOKEN=$(openssl rand -hex 32) \
WHISPER_URL=http://127.0.0.1:8080/inference \
go run ./cmd/mcp
```

Note that ffmpeg must still be installed on the host for the bot to
transcode incoming `.oga` to 16 kHz mono WAV.
