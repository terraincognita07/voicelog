# Roadmap

Public roadmap for self-hosted voicelog. Items here are **planned**, not
**promised** — order changes when reality does. If you want to work on
one, open a discussion or draft PR first so we don't overlap.

For shipped work see [CHANGELOG.md](../CHANGELOG.md). Maintainers keep a
more granular internal short-list (security findings, tech debt) in a
local-only file that does not ship with the repo.

## Next

These are the candidates immediately in front of us. None are committed
to a release date yet.

- ~~**Audit tooling in CI.**~~ Done 2026-05-28. `semgrep` and
  `gitleaks` now run as separate jobs in
  `.github/workflows/ci.yml`, gating merges alongside `govulncheck`.
  `osv-scanner` was tried and dropped — for a Go-only dep tree it
  duplicates what `govulncheck` already does with reachability,
  while adding stdlib-patch-cycle noise.
- ~~**Open-source structural moves.**~~ Done 2026-05-28. `migrations/`
  moved under `internal/db/migrations/`, Dockerfiles relocated to
  `docker/`, `README.md` split into topical files under `docs/`, and
  `internal/db/notes.go` + `internal/mcp/server.go` split by domain.
- ~~**Fuzz corpus**~~ Done 2026-05-28. `go test -fuzz` targets for the
  callback-data parsers, the FTS5 query path, the vocab term, and
  `whisper.Result.Aggregate` (`internal/telegram/state_fuzz_test.go`,
  `internal/db/fuzz_test.go`, `internal/whisper/aggregate_fuzz_test.go`).

Nothing is firmly queued as the immediate next item — see Mid-term.

## Mid-term

- **Multi-user with per-user DB scoping.** Currently single-user by
  design (`ALLOWED_USER_ID` is a single int64). A multi-user
  deployment needs per-user row partitioning, per-user MCP tokens,
  and a separate vocab table. Big lift; only worth doing with a
  concrete second user asking for it.
- **Embeddings / vector search alongside FTS5.** FTS5 is great for
  keywords; semantic search would catch "what was I thinking about my
  insomnia" hitting a note that says "couldn't fall asleep". Open
  question: which model, how to keep it offline-by-default.
- **Web UI for browsing the corpus.** Read-only, opens against the
  same SQLite file. Useful when the user wants to bulk-edit / triage
  away from a chat interface.

## Long-term / speculative

- **DB encryption at rest.** Currently relies on host-disk encryption
  (operator's job). SQLCipher would change the modernc-pure-Go story
  — needs a CGO-or-not decision.
- **Whisper alternatives.** Whisper.cpp is the only ASR right now;
  swapping for a smaller / faster local model (or even GPU-backed)
  should be plug-and-play through `internal/whisper.Client`.
- **Audit-log MCP tool.** `db_health` returns shape but no access
  trail. A read-only audit table would help reconstruct "what did
  Claude pull?" after the fact. Trade-off: more rows to back up.

## Won't do (unless someone really insists)

- **Cloud-hosted SaaS version.** Defeats the whole point — the
  threat model is "your text stays on your disk".
- **Built-in LLM summarization in the bot.** Reasoning lives in
  Claude, called from outside the bot via MCP. That separation is
  the architecture.
