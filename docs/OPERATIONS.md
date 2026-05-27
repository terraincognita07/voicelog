# Operational notes

Day-to-day care of a running voicelog deployment.

## Backups

`./data/voicelog.db` is everything (DB + audio retention if you turned
it on). SQLite WAL mode is on, so a hot copy needs SQLite's online
backup API rather than a plain `cp`:

```bash
sqlite3 data/voicelog.db ".backup '/backup/voicelog-$(date +%F).db'"
```

For retained audio (`AUDIO_DIR`), a recursive `cp -a` is fine — those
files are immutable once written.

## Model swap

```bash
docker compose stop whisper
scripts/fetch-model.sh ggml-medium-q5_0.bin     # or whichever model
# Edit docker-compose.yml: change the -m flag on whisper.command
docker compose up -d whisper
```

No DB migration needed — text transcripts and per-segment confidences
are model-agnostic in voicelog's schema.

## DB maintenance

The mcp container runs `MaintenanceLoop` automatically:

- WAL checkpoint every 7 days (truncates the WAL after a full
  checkpoint so backups stay small)
- `VACUUM` every 30 days (defragments after deletes)

Set `DB_MAINTENANCE_DISABLED=1` if you want to manage these externally.

Ad-hoc health check from any MCP client (or `curl`):

```bash
curl -s -X POST https://voicelog.example.com/mcp \
  -H "Authorization: Bearer $MCP_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"db_health","arguments":{}}}'
```

A healthy DB returns `"integrity_check":"ok"` and `"quick_check":"ok"`.

## Disk growth

- Transcripts are text only. 1000 voice notes ≈ 1 MB of DB.
- With `AUDIO_RETENTION_DAYS = 0` (default), no audio is persisted —
  the temp WAV is deleted right after transcription.
- With retention on, each clip is the original Telegram `.oga`
  (typically 50–500 KB). Multiply by your retention window.
- The bot's disk guard refuses NEW captures cleanly when the data
  filesystem drops below `MIN_FREE_DISK_MB` (default 500). Below that
  threshold, INSERTs would race with SQLite's no-space error path.

## Common operational issues

- **Bot doesn't reply.** `docker compose logs bot`. Most common cause:
  wrong `ALLOWED_USER_ID`, or rejected by Telegram (invalid token).
- **MCP 502 from nginx.** Container probably not running —
  `docker compose ps`. If it IS running, check `docker compose logs mcp`
  for a panic or bind error.
- **First deploy restart loop on fresh DB.** Bot and mcp both try to
  apply migrations and set `journal_mode=WAL` simultaneously; the
  loser's first connection hits `SQLITE_BUSY`. Resolved in code via
  retry-on-busy in `db.Open`. If you see this on an older build,
  staggering the two `docker compose up -d` calls by a couple seconds
  works around it.
- **Audio orphans.** When you change `AUDIO_DIR` or restore an old
  data dir alongside a new DB, the bot logs Warn lines about `.oga`
  files with no matching `notes.audio_path` row on startup. Read-only;
  manual cleanup is your call.
