#!/bin/bash
# Gru hook script for Claude Code.
#
# Claude Code runs hooks in a sanitized environment (no inherited env vars),
# so we derive everything we need from files:
#   - ~/.gru/server.yaml  — API key and server address
#   - <cwd>/.gru/session-id  — GRU session UUID (flat, CWD-local lookup)
#   - <project>/.gru/sessions/<shortID>  — legacy worktree-layout lookup

# Claude Code passes the hook event JSON via stdin.
HOOK_DATA=$(cat)

# Extract cwd from the hook event JSON using Python (always available).
CWD=$(python3 -c "import json,sys; print(json.load(sys.stdin).get('cwd',''))" 2>/dev/null <<< "$HOOK_DATA")
[ -n "$CWD" ] || exit 0

# Resolve the GRU session ID, preferring the CWD-local lookup written by
# every `gru launch` (works for worktree and non-worktree launches alike).
# Fall back to the worktree-convention path for older launches that predate
# the flat file.
GRU_SESSION_ID=""
if [ -f "$CWD/.gru/session-id" ]; then
  GRU_SESSION_ID=$(cat "$CWD/.gru/session-id")
fi
if [ -z "$GRU_SESSION_ID" ]; then
  SHORT_ID=$(basename "$CWD")
  PROJECT_ROOT=$(dirname "$(dirname "$(dirname "$CWD")")")
  SESSION_FILE="$PROJECT_ROOT/.gru/sessions/$SHORT_ID"
  if [ -f "$SESSION_FILE" ]; then
    GRU_SESSION_ID=$(cat "$SESSION_FILE")
  fi
fi
[ -n "$GRU_SESSION_ID" ] || exit 0

# Read connection config from ~/.gru/server.yaml.
# addr is "host:port" or ":port" (host-only listen).
# No auth token is required — the server binds tailnet/loopback only.
CONFIG="$HOME/.gru/server.yaml"
ADDR=$(grep '^addr' "$CONFIG" 2>/dev/null | awk '{print $2}')
GRU_PORT="${ADDR##*:}"
GRU_HOST="${ADDR%:*}"
# If addr is ":7777", the host portion is empty — default to localhost.
GRU_HOST="${GRU_HOST:-localhost}"
GRU_PORT="${GRU_PORT:-7777}"

curl -s -m 2 -X POST \
  "http://$GRU_HOST:$GRU_PORT/events" \
  -H "Content-Type: application/json" \
  -H "X-Gru-Runtime: claude-code" \
  -H "X-Gru-Session-ID: $GRU_SESSION_ID" \
  -d "$HOOK_DATA" &
