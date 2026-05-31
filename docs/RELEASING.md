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
6. **Go Report Card refresh.** Visit
   <https://goreportcard.com/report/github.com/terraincognita07/voicelog>,
   click "Refresh now" (the report is cached per commit and won't
   pick up the release commit until you push it). Confirm the
   grade is still A+ and the 6 sub-checks (gofmt, go_vet, gocyclo,
   ineffassign, license, misspell) all stay 100%. A regression
   on any of them is a release blocker; fix and re-push before
   tagging.
7. **Version bump decision.** Based on the CHANGELOG, pick MAJOR /
   MINOR / PATCH per the rules above. When in doubt, prefer the
   higher bump — easier to explain "we skipped v0.4 because v0.5
   added that one breaking thing" than to retroactively re-tag.
8. **Bump `serverVersion`** in `internal/mcp/server.go` to the version
   you're about to tag. It's the version the MCP server reports to
   clients in the initialize handshake; it's a hardcoded constant, so
   it silently lags unless bumped here, in the same commit as the tag.
   (If this ever becomes a chore, inject it via `-ldflags "-X
   github.com/terraincognita07/voicelog/internal/mcp.serverVersion=…"`
   from `git describe` and make it a `var`.)

## Cutting the tag

Run from a clean working tree on `main`:

```bash
# 1. Move [Unreleased] into a versioned heading.
#    Edit CHANGELOG.md by hand:
#      ## [Unreleased]            -> ## [Unreleased]
#                                    (empty until the next change)
#      (no version heading here)  -> ## [vX.Y.Z] - YYYY-MM-DD
#    Order subsections per Keep-a-Changelog: Added / Changed /
#    Deprecated / Removed / Fixed / Security. Combine any duplicate
#    headings that crept in during the cycle.
git add CHANGELOG.md
git commit -m "release: vX.Y.Z"

# 2. Tag.
git tag -a vX.Y.Z -m "voicelog vX.Y.Z"
git push origin main
git push origin vX.Y.Z
```

Then on GitHub:

1. **Releases → Draft a new release.**
2. Pick the tag.
3. Title: `vX.Y.Z`.
4. Body: paste the new `[vX.Y.Z]` section of CHANGELOG verbatim.
5. **Publish.**

## The CHANGELOG-vs-git history weirdness

In the v0.1.0 → v0.2.0 window the CHANGELOG was edited proactively
with draft `[0.2.0]` and `[0.3.0]` sections, but no matching git
tags were ever pushed. The current state of the repo is:

- `v0.1.0` — real git tag, real release on GitHub
- `[0.2.0]` / `[0.3.0]` — CHANGELOG sections describing work that
  landed on `main` but was never tagged
- `[Unreleased]` — work since `[0.3.0]`

Before cutting the *next* tag, decide how to reconcile this — pick
one and stick with it:

### Option A — sink everything into one big [vNext]

Merge `[0.2.0]` + `[0.3.0]` + `[Unreleased]` into one new section
named after the actual next version (`v0.2.0` if you treat
post-`v0.1.0` as the next MINOR). The history loses the original
batch boundaries, but matches git exactly: one tag per CHANGELOG
section.

**Pick this if:** you want CHANGELOG and `git tag -l` to agree
trivially, and don't mind that the "P0 / P1 / P2 batch" framing
becomes a single big release note.

### Option B — backfill retroactive tags

Put a `v0.2.0` tag on the commit where the P0 batch landed
(2026-05-27, commit predates the F1/F2/F3 work) and `v0.3.0` on
the commit where the P1+P2 batches finished. Then tag the
current `main` as `v0.4.0`. Three new tags appear in `git tag -l`.

**Pick this if:** you value the granular release notes already
written. The tags will all share roughly the same publish date
on GitHub since they're being added in arrears, which can look
slightly weird.

### Option C — intermediate

Tag current `main` as `v0.2.0` and leave the existing `[0.3.0]`
section as a CHANGELOG-only historical record. Rename `[0.3.0]`
→ "Drafted but never released" or similar so future readers
aren't confused. `[Unreleased]` contents merge into the new
`[0.2.0]`.

**Pick this if:** you want CHANGELOG to mostly tell a linear
story but don't want to retro-tag, and are OK explaining the
one orphaned section.

The decision is binary at tag time — pick once, document the
choice in the commit message of the release commit, and move on.

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
- Sweep [.agents/context/todo.md](../.agents/context/todo.md) for
  items that the release closed implicitly. Check them off with a
  one-line note ("done 2026-MM-DD, shipped in vX.Y.Z").
- Announce on whatever channel you use. The GitHub release page is
  the canonical text; everything else links to it.
