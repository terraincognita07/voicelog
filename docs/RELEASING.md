# Releasing voicelog

How to cut a tag, what to check before doing so, and how to handle the
specific weirdness this repo has accumulated around its CHANGELOG.

## Versioning

Loose [SemVer](https://semver.org/):

- **MAJOR** — breaking change that the operator must act on: schema
  migration that cannot roll forward, MCP tool removed, env-var
  renamed, anything that requires a manual data step.
- **MINOR** — new feature, new MCP tool, new env-var added with a
  safe default, new docs surface.
- **PATCH** — bug fix, security fix that doesn't change behaviour,
  doc-only correction.

`[Unreleased]` in [../CHANGELOG.md](../CHANGELOG.md) collects work
until the next tag. At tag time it migrates into a versioned heading
with today's date.

## Pre-release checklist

In order. Do not skip steps — release gates only work if they actually
gate.

1. **Local gates green.** `make ci` (vet + staticcheck + build +
   race-tests + govulncheck) passes against `main`.
2. **CI green on `main`.** The badge in README must be passing for
   the commit you intend to tag. Open a fresh branch first if you
   need to land a fix.
3. **Audit batch in CI.** Two additional jobs in
   `.github/workflows/ci.yml` complement `govulncheck`:
   - `semgrep` — pattern-based findings against `p/golang` and
     `p/security-audit` rulesets. Pinned via the
     `semgrep/semgrep:1.95.0` container.
   - `gitleaks` — full-history committed-secret scan.
   Both are required to be green before a tag. If a finding
   appears, triage it as part of the release work; do not bypass
   by removing the job.

   (An `osv-scanner` job was tried and removed — see the comment
   block in `ci.yml`. Short version: for a Go-only dep tree
   `govulncheck` already covers the OSV database with
   reachability, and `osv-scanner` adds noise from every stdlib
   patch's fresh batch of GO-YYYY-NNNN vulns.)
4. **Manual smoke.** `docker compose up -d` against a fresh
   `./data/`. Send a voice message; verify the bot replies; verify
   `/pending` shows it; call the `db_health` MCP tool and confirm
   `integrity_check = "ok"`. This catches integration regressions
   that unit tests can miss.

   For the MCP side specifically, `scripts/smoke-mcp.sh` walks every
   read-only tool against a live server and asserts JSON-RPC envelope
   shape. Run it after `docker compose up -d`:

   ```bash
   MCP_TOKEN="$(grep ^MCP_TOKEN= .env | cut -d= -f2)" \
   MCP_URL=http://127.0.0.1:8081/mcp \
   bash scripts/smoke-mcp.sh
   ```

   To exercise the mutating tools (`mark_analyzed` / `delete_notes`),
   pass `--mutate` and `NOTE_ID=<id>` for a throwaway seed note —
   `delete_notes` permanently removes it.
5. **CHANGELOG accuracy.** Read `[Unreleased]` end-to-end. Anything
   shipped since the previous tag that is *user-visible* (env-var,
   MCP tool, bot UI, docs file moved) must be listed. Drop entries
   that turned out to be internal-only and got described too
   enthusiastically.
6. **Lint gate.** `make lint` (golangci-lint, pinned in CI) must be
   clean. It replaced the retired Go Report Card check and covers the
   same ground and more (gofmt, staticcheck, ineffassign, misspell,
   errcheck, unused). A finding here is a release blocker; fix and
   re-push before tagging.
7. **Version bump decision.** Based on the CHANGELOG, pick MAJOR /
   MINOR / PATCH per the rules above. When in doubt, prefer the
   higher bump — easier to explain "we skipped v0.4 because v0.5
   added that one breaking thing" than to retroactively re-tag.
8. **`serverVersion` no longer needs a manual bump.** It's injected at
   build time from `git describe` via `-ldflags` (wired in the Makefile
   and `docker/Dockerfile.mcp`; `serverVersion` is now a `var` defaulting
   to `"dev"`). The only requirement is **build the release images after
   the tag exists** — `git describe` on a tagged commit yields the bare
   tag, so the handshake matches automatically. Build with the version in
   the shell env:

   ```bash
   VERSION=$(git describe --tags --dirty | sed 's/^v//') \
     docker compose up -d --build
   ```

   A plain `go build` / un-stamped image reports `"dev"`, which is the
   honest answer for an untagged build. This closes the recurring drift
   that hit v0.5.0 and v0.8.1 (the hand-bump kept getting forgotten).

## Cutting the tag

`main` only accepts changes through a pull request (repository ruleset),
so the release commit rides a short-lived branch:

```bash
# 1. Move [Unreleased] into a versioned heading.
#    Edit CHANGELOG.md by hand:
#      ## [Unreleased]            -> ## [Unreleased]
#                                    (empty until the next change)
#      (no version heading here)  -> ## [vX.Y.Z] - YYYY-MM-DD
#    Order subsections per Keep-a-Changelog: Added / Changed /
#    Deprecated / Removed / Fixed / Security. Combine any duplicate
#    headings that crept in during the cycle.
git switch -c release/vX.Y.Z
git add CHANGELOG.md
git commit -m "release: vX.Y.Z"
git push -u origin release/vX.Y.Z
# open a PR, wait for CI, squash-merge it

# 2. Tag the squash-merge commit on main (NOT the branch commit —
#    squash rewrites the SHA).
git switch main && git pull origin main
git tag -a vX.Y.Z -m "voicelog vX.Y.Z"
git push origin vX.Y.Z
```

Then on GitHub:

1. **Releases → Draft a new release.**
2. Pick the tag.
3. Title: `vX.Y.Z`.
4. Body: paste the new `[vX.Y.Z]` section of CHANGELOG verbatim.
5. **Publish.**

## Historical: the CHANGELOG-vs-git reconciliation (resolved at v0.2.0)

In the v0.1.0 → v0.2.0 window the CHANGELOG was edited proactively
with draft `[0.2.0]` and `[0.3.0]` sections, but no matching git
tags were ever pushed, so the CHANGELOG and `git tag -l` disagreed.

This was resolved via **Option A**: at v0.2.0 the orphaned
`[0.2.0]` + `[0.3.0]` + `[Unreleased]` sections were merged into a
single `[0.2.0]` release note (the CHANGELOG entry itself records
this — "option A from docs/RELEASING.md"). Since then the invariant
holds: one git tag per CHANGELOG section. Tags now run `v0.1.0` …
`v0.8.1` with one section each (the skipped `v0.4` is intentional —
see the version-bump note above).

Nothing to decide here anymore — kept as a record of why the early
CHANGELOG history looks the way it does.

## Hotfix / patch release

For a bug discovered after a tag:

1. Branch from the tag: `git checkout -b hotfix/vX.Y.(Z+1) vX.Y.Z`
2. Land the fix.
3. Update CHANGELOG: add `[vX.Y.(Z+1)]` between `[Unreleased]` and
   `[vX.Y.Z]`.
4. Tag the hotfix branch.
5. Cherry-pick or merge the fix into `main` (CHANGELOG entry stays
   on main as `Unreleased` until next minor / major rolls it up).

Same publish flow on GitHub.

## Post-release

- Update [docs/ROADMAP.md](ROADMAP.md) — move shipped items off the
  "Next" list. If a roadmap item moved versions (planned for v0.3.0
  but landed in v0.2.0), edit the description rather than leaving
  the wrong version next to it.
- Sweep your local tracking notes (maintainers: the internal
  `.agents/` brain) for items the release closed implicitly. Check
  them off with a one-line note ("done 2026-MM-DD, shipped in
  vX.Y.Z").
- Announce on whatever channel you use. The GitHub release page is
  the canonical text; everything else links to it.
