#!/usr/bin/env bash
# Smoke-test a running voicelog MCP server. Walks every tool with a tiny
# happy-path payload and asserts the JSON-RPC response shape. Intended
# for the pre-release manual gate in docs/RELEASING.md — the unit tests
# already exercise these tools against an httptest.Server fixture, but
# a separate harness against the actual binary catches wiring regressions
# (auth wrapper, port binding, env-var plumbing).
#
# Requires curl + jq. Both are in every standard dev image.
#
# Usage:
#   MCP_URL=http://127.0.0.1:8081/mcp MCP_TOKEN=<token> scripts/smoke-mcp.sh
#
# The script does NOT mutate state by default: list_pending_notes,
# get_notes_in_range, search_notes, get_note, db_health. Mutating tools
# (mark_analyzed, discard_notes, restore_note, retranscribe) require
# --mutate to be passed AND a seeded note id in NOTE_ID — those touch
# real rows.
#
# Exit codes:
#   0 — every checked tool returned a JSON-RPC success envelope with the
#       expected fields
#   1 — at least one tool failed (HTTP error, JSON-RPC error, or a
#       missing field). Detailed failure logged before exit.

set -euo pipefail

MCP_URL="${MCP_URL:-http://127.0.0.1:8081/mcp}"
MCP_TOKEN="${MCP_TOKEN:-}"
MUTATE="${1:-}"
NOTE_ID="${NOTE_ID:-}"

if [ -z "$MCP_TOKEN" ]; then
  echo "FAIL: MCP_TOKEN env var is required" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "FAIL: jq not on PATH" >&2
  exit 1
fi

# call_tool $name $args_json
# Sends a JSON-RPC tools/call, prints the raw body (after decoding SSE if
# the transport replied that way), and returns the body as a global var
# RESP for further jq queries.
call_tool() {
  local name="$1"
  local args="$2"
  local body
  body=$(jq -n --arg n "$name" --argjson a "$args" \
    '{jsonrpc:"2.0", id:1, method:"tools/call", params:{name:$n, arguments:$a}}')

  local raw
  raw=$(curl -sS --fail-with-body \
    -H "Authorization: Bearer $MCP_TOKEN" \
    -H "Content-Type: application/json" \
    -H "Accept: application/json, text/event-stream" \
    -X POST --data "$body" "$MCP_URL") || {
      echo "FAIL: $name — HTTP request failed" >&2
      echo "$raw" >&2
      return 1
    }

  # If transport answered with SSE, pick the JSON-RPC payload from the
  # first 'data:' line. Otherwise raw is already the payload.
  if printf '%s' "$raw" | grep -q '^data:'; then
    RESP=$(printf '%s' "$raw" | sed -n 's/^data: \?//p' | head -n1)
  else
    RESP="$raw"
  fi

  # Surface JSON-RPC errors loudly.
  local err
  err=$(printf '%s' "$RESP" | jq -r '.error.message // empty')
  if [ -n "$err" ]; then
    echo "FAIL: $name — JSON-RPC error: $err" >&2
    return 1
  fi
}

# expect_isError $name $want   — assert the tool's IsError flag.
expect_isError() {
  local name="$1"
  local want="$2"
  local got
  got=$(printf '%s' "$RESP" | jq -r '.result.isError // false')
  if [ "$got" != "$want" ]; then
    echo "FAIL: $name — isError = $got, want $want" >&2
    printf '%s\n' "$RESP" >&2
    return 1
  fi
}

# expect_content_has $name $jq_path — assert the first content text
# (a JSON string) decodes to an object that satisfies $jq_path != null.
expect_content_has() {
  local name="$1"
  local path="$2"
  local inner
  inner=$(printf '%s' "$RESP" | jq -r '.result.content[0].text')
  if [ -z "$inner" ] || [ "$inner" = "null" ]; then
    echo "FAIL: $name — content[0].text is empty/null" >&2
    return 1
  fi
  if ! printf '%s' "$inner" | jq -e "$path" >/dev/null 2>&1; then
    echo "FAIL: $name — content does not satisfy $path" >&2
    echo "content was: $inner" >&2
    return 1
  fi
}

PASS=0
FAIL=0

run() {
  local name="$1"
  shift
  if "$@"; then
    echo "OK   $name"
    PASS=$((PASS + 1))
  else
    FAIL=$((FAIL + 1))
  fi
}

# --- Auth + transport sanity --------------------------------------------

# Missing token → 401 (BearerAuth in internal/mcp/auth.go).
auth_missing_returns_401() {
  local code
  code=$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "Content-Type: application/json" \
    -X POST --data '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' "$MCP_URL")
  [ "$code" = "401" ] || { echo "FAIL: missing token — got HTTP $code, want 401" >&2; return 1; }
}
run "auth: missing token rejected" auth_missing_returns_401

# Wrong token → 401.
auth_wrong_returns_401() {
  local code
  code=$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "Authorization: Bearer obviously-not-the-right-token" \
    -H "Content-Type: application/json" \
    -X POST --data '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' "$MCP_URL")
  [ "$code" = "401" ] || { echo "FAIL: wrong token — got HTTP $code, want 401" >&2; return 1; }
}
run "auth: wrong token rejected" auth_wrong_returns_401

# --- tools/list ---------------------------------------------------------

tools_list_returns_known_tools() {
  local body
  body=$(curl -sS --fail-with-body \
    -H "Authorization: Bearer $MCP_TOKEN" \
    -H "Content-Type: application/json" \
    -H "Accept: application/json, text/event-stream" \
    -X POST --data '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' "$MCP_URL")
  if printf '%s' "$body" | grep -q '^data:'; then
    body=$(printf '%s' "$body" | sed -n 's/^data: \?//p' | head -n1)
  fi
  for tool in list_pending_notes get_notes_in_range search_notes get_note \
              mark_analyzed discard_notes restore_note retranscribe db_health; do
    if ! printf '%s' "$body" | jq -e --arg t "$tool" '.result.tools | map(.name) | index($t)' >/dev/null; then
      echo "FAIL: tools/list missing $tool" >&2
      return 1
    fi
  done
}
run "tools/list: every voicelog tool registered" tools_list_returns_known_tools

# --- Read-only tools ----------------------------------------------------

run "list_pending_notes" bash -c '
  call_tool list_pending_notes "{\"limit\": 5}" &&
  expect_isError list_pending_notes false &&
  expect_content_has list_pending_notes "type==\"array\""
'

run "get_notes_in_range (24h window)" bash -c '
  from=$(date -u -d "24 hours ago" +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null || date -u -v -24H +"%Y-%m-%dT%H:%M:%SZ")
  to=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  call_tool get_notes_in_range "{\"from\": \"$from\", \"to\": \"$to\"}" &&
  expect_isError get_notes_in_range false &&
  expect_content_has get_notes_in_range "type==\"array\""
'

run "search_notes (empty corpus is fine)" bash -c '
  call_tool search_notes "{\"query\": \"smoketest_noresults_term_${RANDOM}\"}" &&
  expect_isError search_notes false &&
  expect_content_has search_notes "type==\"array\" and length==0"
'

run "db_health (quick mode)" bash -c '
  call_tool db_health "{\"quick\": true}" &&
  expect_isError db_health false &&
  expect_content_has db_health ".quick_check==\"ok\""
'

# get_note with an obviously-missing id — expect a tool-level error.
run "get_note (missing id surfaces tool error)" bash -c '
  call_tool get_note "{\"id\": 999999999}" &&
  expect_isError get_note true
'

# --- Mutating tools (opt-in, requires a real note id) -------------------

if [ "$MUTATE" = "--mutate" ]; then
  if [ -z "$NOTE_ID" ]; then
    echo "SKIP: --mutate passed but NOTE_ID env var is empty" >&2
  else
    run "mark_analyzed (idempotent)" bash -c "
      call_tool mark_analyzed '{\"ids\": [$NOTE_ID]}' &&
      expect_isError mark_analyzed false
    "
    # Discarding + restoring round-trips the note's state so the smoke
    # test leaves no side effect besides one extra history row.
    run "discard_notes ($NOTE_ID)" bash -c "
      call_tool discard_notes '{\"ids\": [$NOTE_ID]}' &&
      expect_isError discard_notes false
    "
    run "restore_note ($NOTE_ID)" bash -c "
      call_tool restore_note '{\"id\": $NOTE_ID}' &&
      expect_isError restore_note false
    "
  fi
else
  echo "SKIP mutating tools — pass --mutate plus NOTE_ID=<id> to exercise them"
fi

# --- Summary ------------------------------------------------------------

echo
echo "Smoke check: $PASS passed, $FAIL failed"
if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
