<!--
Thanks for the PR. Keep this template lean — delete sections that don't
apply. Read CONTRIBUTING.md before opening if you haven't.
-->

## Summary

<!-- 1–3 bullets. What changed, why. -->

## Touched layers

<!-- Tick what you actually touched. Helps reviewers pick the right hat. -->

- [ ] `cmd/` (entrypoints, wiring)
- [ ] `internal/telegram` (bot surface)
- [ ] `internal/mcp` (MCP tools)
- [ ] `internal/db` (SQL, migrations)
- [ ] `internal/whisper`
- [ ] `internal/audio` / `internal/diskguard`
- [ ] `internal/db/migrations/` (schema change)
- [ ] CI / Dockerfiles / docker-compose
- [ ] docs only

## Risk

<!-- Pick one and elaborate. -->

- [ ] **Low** — local refactor, isolated test update, doc tweak.
- [ ] **Medium** — multi-file refactor, bot UI change, new MCP tool, new env var.
- [ ] **High** — schema migration, destructive operation, auth/security
      surface, public contract change.

<!-- For Medium/High: name the rollback path. For schema changes:
     explicitly name how to undo. -->

## Test plan

<!-- What you ran locally; what should be tested manually after deploy. -->

- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `staticcheck ./...`
- [ ] `govulncheck ./...`
- [ ] Manual Telegram flow (if bot UI changed)
- [ ] Manual MCP call (if MCP changed) — `curl` or via Claude

## Security / threat-model impact

<!-- Required if any of: auth, MCP token, error paths, logging, file
     storage, env var, dependency, supply chain. Otherwise "none". -->

## Checklist

- [ ] Locale strings (if any): both `en` and `ru`, locale test passes.
- [ ] Error labels (if any): both locales' `Errors` maps updated.
- [ ] Schema change (if any): new migration file; rollback path in commit body.
- [ ] README env-vars table updated (if new env).
- [ ] CHANGELOG entry in "Unreleased" section.
