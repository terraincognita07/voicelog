# Security model

This document describes voicelog's threat model and what's
intentionally NOT protected. For vulnerability disclosure see
[../SECURITY.md](../SECURITY.md).

## What IS protected

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
- **govulncheck gate.** `govulncheck@v1.1.4` runs in CI on every PR/push;
  any reachable vulnerability blocks the merge. Pinned version so a
  surprise tool update can't silently change the rule set.

## What's NOT protected — read this if you self-host

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
