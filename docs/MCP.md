# MCP — tools and Claude.ai exposure

The mcp container exposes 12 tools over JSON-RPC at `/mcp`, guarded by
a bearer token (`MCP_TOKEN`). It binds to `127.0.0.1:8081` on the
host — Internet exposure is your reverse-proxy's job.

## Tools

Every request must be authenticated — either via
`Authorization: Bearer $MCP_TOKEN` on the `/mcp` route, or via the
token-in-URL pattern on `/t/<token>/mcp` (see [Claude.ai web setup](#claudeai-web--needs-public-https) below).

- **`list_pending_notes(limit?: int = 50)`** —
  last N notes with `status='pending'`, newest first
- **`get_notes_in_range(from: ISO8601, to: ISO8601, status?: string, include_discarded?: bool = false, limit?: int = 500)`** —
  date-window query, optional status filter (`pending|analyzed|discarded`).
  Discarded notes are excluded by default; pass `include_discarded=true` or
  `status="discarded"` to surface them. Hard cap 500 rows per response.
- **`search_notes(query: string, limit?: int = 20, include_discarded?: bool = false)`** —
  SQLite FTS5 MATCH. Supports bare words (AND), `"phrase"`, `term*`,
  `term1 OR term2`. Bare Cyrillic words are automatically stemmed and
  prefix-matched (Russian morphology) — searching `работа` also finds
  `работе`/`работу`; Latin terms match exactly. Results sorted by bm25
  rank (lower = better). Each hit
  includes a `snippet` field — ~30 tokens around the match with the
  matched term wrapped in `<<` / `>>` and elided context shown as `...`.
  Discarded notes are filtered out by default; opt in via `include_discarded`.
- **`get_note(id: int)`** —
  fetch one full note by id. Returns the note object or an error if the
  id is unknown. Every note returned by MCP carries `confidence_overall`,
  `confidence_min` (mean / worst whisper `avg_logprob` — closer to 0 is
  more confident; `null` if the note was created before the verbose-JSON
  pipeline), and `suspect_hallucination` (bool — first whisper segment
  exceeded the silence-probability threshold).
- **`mark_analyzed(ids: int[])`** —
  flip status to `analyzed` for the given ids. Discarded notes are
  not touched. Returns `{updated: N}`.
- **`discard_notes(ids: int[])`** —
  mark the given ids as discarded (batch parity with the bot's `/delete`).
  Already-discarded rows are ignored. Returns `{updated: N}`. Reversible
  via `restore_note`.
- **`restore_note(id: int)`** —
  flip a single discarded note back to `pending`. Returns
  `{restored: bool}` — `true` if it was discarded and got restored,
  `false` if it exists but was not in `discarded` state.
- **`db_health(quick?: bool = false)`** —
  runs SQLite `PRAGMA integrity_check` + `quick_check`, then reports
  `note_count` and `db_size_bytes`. Healthy DB: both checks return the
  literal `"ok"`. Cheap on the typical voicelog DB — safe to invoke
  ad-hoc (e.g. weekly: "how's my DB?"). Pass `quick=true` on a
  multi-GB DB to skip the full page-scan integrity_check (which can
  take >30s); `integrity_check` then returns the sentinel `"skipped"`
  and `quick_check` still runs.
- **`retranscribe(id: int)`** —
  re-run whisper on the note's retained audio file (requires
  `AUDIO_RETENTION_DAYS > 0` on the bot side, AND the note's audio still
  on disk). The previous `raw_text` is archived in `notes_history`
  before the row is updated, so the change is reversible at the SQL
  level. Returns `{note_id, old_text, new_text, confidence_overall,
  confidence_min, suspect_hallucination}` so the caller can summarize
  the diff. Discarded notes are refused — call `restore_note` first
  if you really want to overwrite them. Requires the mcp container
  to be wired with `WHISPER_URL`.
- **`list_vocab()`** —
  list the current whisper vocabulary terms (newest first). Returns
  `{terms: [...], count: N}`. Use before `add_vocab` to avoid duplicates.
- **`add_vocab(terms: string[])`** —
  add terms to the whisper vocabulary so future transcriptions recognize
  them. Use when you notice whisper repeatedly mis-spelling the same name
  or jargon across notes. Case preserved; duplicates ignored
  case-insensitively; terms over 64 chars skipped. Returns
  `{added, skipped_existing, skipped_too_long}`.
- **`remove_vocab(term: string)`** —
  remove a single term (case-insensitive). Returns `{removed: bool}`.
  Wiping the whole vocabulary is intentionally bot-only (`/vocab clear`).

Tool schemas are visible via standard MCP `tools/list`.

## Asking Claude (example prompts)

You drive voicelog from any Claude conversation in plain language — you don't
name the tools, Claude picks them. Not sure what's possible? Just ask Claude
**"what can you do with voicelog?"** and it lists its tools — the chat
equivalent of looking at the bot's buttons.

Read / analyze:

- *"what did I record yesterday / last week / in May?"* → `get_notes_in_range`
- *"find notes about insomnia"*, *"when did I mention Kolya?"* → `search_notes`
  (Cyrillic terms are stemmed, so dictionary forms match inflected ones)
- *"show my pending notes"*, *"what's in my queue?"* → `list_pending_notes`
- *"show note 42 in full"* → `get_note`
- *"check the database integrity"* → `db_health`

Write (Claude will do these, but they change your journal):

- *"mark 12 and 13 as analyzed"* → `mark_analyzed`
- *"discard notes 5 and 6"* / *"restore note 7"* → `discard_notes` / `restore_note`
- *"re-transcribe note 42"* → `retranscribe` (only if audio retention is on
  and the file hasn't aged out)
- *"whisper keeps misspelling 'Иннокентий' — add it to the vocabulary"* →
  `add_vocab`; *"what's in the vocabulary?"* → `list_vocab`;
  *"remove X from the vocabulary"* → `remove_vocab`

What Claude **can't** do from chat (by design — so you don't wait on it):

- **Edit a note's text directly.** There is no MCP edit tool on purpose; fix
  the text with the bot's `✏️ Edit` button, or ask Claude to `retranscribe`
  (re-runs whisper on the retained audio).
- **Wipe the whole vocabulary.** Bot-only (`/vocab clear`, two-step confirm).
- **Create a brand-new note from chat.** You populate the journal (voice or
  text in the bot) so Claude's analysis never mixes with your own entries.

## Claude.ai web — needs public HTTPS

Claude.ai's *Add custom connector* dialog currently exposes **only OAuth
fields** — there is no UI to set a custom `Authorization` header. To use
it with our bearer-token server, put the token into the URL itself and
let the reverse proxy inject the header before it reaches mcp.

The configs below expose two routes side by side:

| Route | Auth | Use case |
|---|---|---|
| `/mcp` | `Authorization: Bearer <token>` header | Claude Code, programmatic clients, monitoring |
| `/t/<token>/mcp` | none (token is in URL) | Claude.ai web |

Pick **nginx** or **Traefik**. For Traefik, pick docker-labels or file-provider
depending on how your Traefik is configured.

### nginx

```nginx
# Substitute YOUR_MCP_TOKEN and voicelog.example.com below.
server {
    listen 443 ssl http2;
    server_name voicelog.example.com;

    ssl_certificate     /etc/letsencrypt/live/voicelog.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/voicelog.example.com/privkey.pem;

    # Header-based route — Bearer required from the client.
    location = /mcp {
        proxy_pass http://127.0.0.1:8081;
        proxy_http_version 1.1;
        proxy_buffering off;
        proxy_read_timeout 600s;
        proxy_set_header Authorization $http_authorization;
    }

    # Path-based route — token in URL, nginx injects the header.
    location = /t/YOUR_MCP_TOKEN/mcp {
        proxy_pass http://127.0.0.1:8081/mcp;
        proxy_http_version 1.1;
        proxy_buffering off;
        proxy_read_timeout 600s;
        proxy_set_header Authorization "Bearer YOUR_MCP_TOKEN";
    }
}
```

### Traefik — docker-provider (labels)

In `docker-compose.override.yml`:

```yaml
services:
  mcp:
    ports: !reset []
    networks:
      - default
      - traefik          # your Traefik docker network
    labels:
      - "traefik.enable=true"
      - "traefik.docker.network=traefik"

      # Router 1: /mcp with Bearer
      - "traefik.http.routers.voicelog-mcp.rule=Host(`voicelog.example.com`) && Path(`/mcp`)"
      - "traefik.http.routers.voicelog-mcp.entrypoints=websecure"
      - "traefik.http.routers.voicelog-mcp.tls.certresolver=letsencrypt"
      - "traefik.http.routers.voicelog-mcp.service=voicelog-mcp"

      # Router 2: /t/<token>/mcp without header
      - "traefik.http.routers.voicelog-mcp-path.rule=Host(`voicelog.example.com`) && PathPrefix(`/t/YOUR_MCP_TOKEN/mcp`)"
      - "traefik.http.routers.voicelog-mcp-path.entrypoints=websecure"
      - "traefik.http.routers.voicelog-mcp-path.tls.certresolver=letsencrypt"
      - "traefik.http.routers.voicelog-mcp-path.middlewares=voicelog-stripprefix,voicelog-addauth"
      - "traefik.http.routers.voicelog-mcp-path.service=voicelog-mcp"

      - "traefik.http.middlewares.voicelog-stripprefix.stripprefix.prefixes=/t/YOUR_MCP_TOKEN"
      - "traefik.http.middlewares.voicelog-addauth.headers.customrequestheaders.Authorization=Bearer YOUR_MCP_TOKEN"

      - "traefik.http.services.voicelog-mcp.loadbalancer.server.port=8081"

networks:
  traefik:
    external: true
    name: traefik
```

### Traefik — file-provider (`dynamic/voicelog.yml`)

```yaml
http:
  routers:
    voicelog-mcp:
      rule: "Host(`voicelog.example.com`) && Path(`/mcp`)"
      entryPoints: [websecure]
      tls:
        certResolver: letsencrypt
      service: voicelog-mcp

    voicelog-mcp-path:
      rule: "Host(`voicelog.example.com`) && PathPrefix(`/t/YOUR_MCP_TOKEN/mcp`)"
      entryPoints: [websecure]
      tls:
        certResolver: letsencrypt
      middlewares: [voicelog-stripprefix, voicelog-addauth]
      service: voicelog-mcp

  middlewares:
    voicelog-stripprefix:
      stripPrefix:
        prefixes: ["/t/YOUR_MCP_TOKEN"]
    voicelog-addauth:
      headers:
        customRequestHeaders:
          Authorization: "Bearer YOUR_MCP_TOKEN"

  services:
    voicelog-mcp:
      loadBalancer:
        servers:
          - url: "http://voicelog-mcp:8081"
```

Smoke-test both routes from outside the server:

```bash
# Header-based
curl -s -X POST https://voicelog.example.com/mcp \
  -H "Authorization: Bearer YOUR_MCP_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | jq '.result.tools[].name'

# Path-based
curl -s -X POST https://voicelog.example.com/t/YOUR_MCP_TOKEN/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | jq '.result.tools[].name'
```

Both must return the same set of tool names.

### Register in Claude.ai

Settings → Connectors → **Add custom connector**:

- **Name:** `voicelog`
- **Remote MCP server URL:** `https://voicelog.example.com/t/YOUR_MCP_TOKEN/mcp`
- **OAuth fields:** leave blank

Open a new chat — Claude sees `voicelog__*` tools.

## Tradeoffs of path-based auth

Putting the token into the URL means it appears in:

- the reverse-proxy access log
- browser history if you ever paste the URL into a browser
- `docker inspect` for the proxy (labels mode) or the dynamic YAML (file mode)

For personal self-host these are usually acceptable. To mitigate:

- Prefer Claude Code over Claude.ai web (no token in URL — see the
  Quick-start section in [README](../README.md))
- Rotate the token periodically: `openssl rand -hex 32`, update `.env`
  **and** the proxy config, then `docker compose up -d mcp` + reload the
  proxy
