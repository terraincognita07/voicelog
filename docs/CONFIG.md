# Configuration reference

All voicelog configuration is passed via environment variables (or
`.env` for `docker compose`). Required values fail-fast at startup if
missing; optional ones fall back to defaults documented below.

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `BOT_TOKEN` | yes | — | Telegram bot token from BotFather |
| `ALLOWED_USER_ID` | yes | — | The only Telegram user ID allowed to talk to the bot |
| `DB_PATH` | yes | — | SQLite file path inside the container, e.g. `/data/voicelog.db` |
| `WHISPER_URL` | yes | — | Whisper `/inference` endpoint, e.g. `http://whisper:8080/inference`. Required by the bot; the mcp container runs without it (then `retranscribe` is disabled). |
| `MCP_TOKEN` | yes | — | Bearer token for the MCP server. Min 16 chars. Use `openssl rand -hex 32`. |
| `MCP_PORT` | no | `8081` | Port the mcp container listens on inside the host |
| `TZ` | no | UTC | Timezone for log timestamps and bot replies |
| `BOT_LOCALE` | no | `en` | Bot reply language: `en` or `ru`. Commands are unchanged in any locale. |
| `WHISPER_PROMPT` | no | — | Optional whisper "initial prompt" (admin default). User-managed vocabulary (`/vocab`) is appended after this. |
| `AUDIO_RETENTION_DAYS` | no | `0` | If `> 0`, keep the original `.oga` voice file at `AUDIO_DIR/<note_id>.oga` for that many days. A background janitor sweeps every 6h. `0` (default) disables retention — audio is deleted right after transcription. |
| `AUDIO_DIR` | no | `/data/audio` | Where retained `.oga` files live. Only consulted when `AUDIO_RETENTION_DAYS > 0`. |
| `HALLUCINATION_THRESHOLD` | no | `0.6` | Whisper hallucination detector cutoff (float ∈ [0, 1]). First segment's `no_speech_prob` above this flags the note as suspect. Raise to be stricter, lower to be looser. |
| `MIN_FREE_DISK_MB` | no | `500` | Bot refuses new captures when free space on the data filesystem drops below this. `0` disables the guard. |
| `DB_MAINTENANCE_DISABLED` | no | — | When set to any non-empty value, the mcp container skips its weekly WAL checkpoint + monthly VACUUM loop. Use only if you run maintenance externally. |
| `HOST_UID` | no | `1000` | UID of bot/mcp processes — must own `./data` on host |
| `HOST_GID` | no | `1000` | GID of bot/mcp processes — must own `./data` on host |
| `PPROF_ADDR` | no | — | When set to a loopback `host:port` (e.g. `127.0.0.1:6060`), starts `net/http/pprof` on that address for the binary. Any non-loopback bind is refused at startup. Unset = disabled (production default). |

## Notes

- **Token rotation** for `MCP_TOKEN`: generate a new value
  (`openssl rand -hex 32`), update `.env` AND the reverse-proxy config
  if you use path-based exposure (see [MCP.md](MCP.md)), then
  `docker compose up -d mcp` and reload the proxy.
- **Audio retention** is OFF by default. Turning it on
  (`AUDIO_RETENTION_DAYS=N`) enables the `retranscribe` MCP tool and
  lets the bot dedupe accidental double-taps within ~5 minutes.
- **Disk guard.** When the data filesystem drops below
  `MIN_FREE_DISK_MB`, the bot refuses NEW captures cleanly (with a
  user-visible message) instead of letting SQLite hit a no-space
  error half-way through an INSERT. Set to `0` to disable in dev.
- **`PPROF_ADDR` is loopback-only by design.** pprof endpoints expose
  goroutine stacks, allocation sites, and source line info (via
  `/debug/pprof/symbol`) — effectively read-only RCE surface if
  reached by a stranger. The startup check refuses `:6060`,
  `0.0.0.0:N`, public IPs, and arbitrary hostnames; only `localhost`,
  `127.0.0.0/8`, and `[::1]` pass. For remote profiling, SSH-tunnel
  to the bound port instead.
