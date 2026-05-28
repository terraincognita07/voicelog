# Contributing to voicelog

Thanks for considering a contribution. voicelog is a personal, self-hosted
tool — it deliberately stays small. PRs that broaden scope into multi-tenant
/ SaaS / health-compliance territory will likely be declined; PRs that
sharpen the existing single-user surface are very welcome.

## Before you start

- Open or comment on an issue first if your change is non-trivial. Avoid
  surprise-PRs over 200 lines.
- Read `README.md` § "Security model" — the threat-model boundary is
  intentional. Changes that quietly raise scope (e.g. opening MCP without
  bearer, exposing audio paths in tool responses) won't be merged without
  a discussion.

## Local setup

Requires Go 1.26+, Docker + Compose v2, and `ffmpeg` if you intend to
exercise the voice path end-to-end.

```bash
git clone https://github.com/terraincognita07/voicelog.git
cd voicelog
go mod download
CGO_ENABLED=0 go build ./...
go test ./...
```

Run the linters CI runs:

```bash
go vet ./...
staticcheck ./...                # https://staticcheck.io
go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
govulncheck ./...
```

For a live dev loop:

```bash
cp .env.example .env             # fill BOT_TOKEN, ALLOWED_USER_ID, MCP_TOKEN
docker compose up -d whisper     # whisper.cpp container only
go run ./cmd/bot                 # bot on host, faster iteration than docker
```

## Code conventions

- **Layering** (strict): `cmd/` only wiring; `internal/telegram` only Telegram
  surface; `internal/mcp` only MCP surface; `internal/db` owns all SQL.
  No cross-layer shortcuts. See `ARCHITECTURE.md`.
- **Errors**: never send raw `err.Error()` to chat. Use
  `userErrMsg(label, err)` → `errReply` (new message) / `errToast`
  (callback popup). Raw err goes to `slog.Error` only. Add new labels to
  the `Errors` map in `internal/telegram/locale.go` (both en and ru).
- **Locale**: every user-facing string lives in `internal/telegram/locale.go`
  with both `en` and `ru` entries. `locale_test.go::TestLocalesAreComplete`
  will fail otherwise.
- **DB schema** changes go through **forward-only** SQL migrations in
  `migrations/`. Don't edit a shipped migration; add a new file. The
  commit body MUST name the rollback path even when trivial.
- **MCP tool annotations**: every new tool sets `ReadOnlyHint`,
  `DestructiveHint`, `IdempotentHint`, `OpenWorldHint` explicitly.
- **Callback data** is capped at 64 bytes. New stateful inline views must
  thread their state through every button's `Data` and pass through whitelist
  validators (`validRecentFilter`, `validDateKey`, `clampPage`).

## Tests

- Unit tests live next to source (`foo_test.go` beside `foo.go`).
- Run `go test ./...` before pushing. `-race` is required for CI; locally
  it needs CGO so install gcc/clang once.
- Coverage isn't a gate but new code shouldn't lower the package's coverage
  noticeably.
- Golden tests for rendering live in `internal/telegram/list_view_test.go`
  — extend rather than replace.

## Commits

- One logical change per commit. Squash trivial fixups locally before pushing.
- Subject line under 70 chars, imperative ("Add X", not "Added X").
- Body: explain the **why** + non-obvious tradeoffs. For schema changes,
  name the rollback path. For security-sensitive changes, explicitly call
  out the threat-model impact.
- Sign off / DCO not required.

## Pull requests

- Fill out the PR template (`.github/PULL_REQUEST_TEMPLATE.md`).
- CI must pass: `go vet`, `staticcheck`, `go test -race`, `govulncheck`,
  docker builds.
- Bot UI changes: include a short "what the user sees" description or
  screenshot.

## Security

For vulnerabilities, **do not open a public issue**. Use GitHub private
vulnerability reporting — see `SECURITY.md`.

## Code of conduct

Be civil. We use the Contributor Covenant — see `CODE_OF_CONDUCT.md`.
