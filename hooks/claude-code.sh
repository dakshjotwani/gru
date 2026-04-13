#!/bin/bash
# Gru hook script for Claude Code.
#
# Claude Code runs hooks in a sanitized environment (no inherited env vars),
# so we derive everything we need from files:
#   - ~/.gru/server.yaml  — API key and server address
#   - <project>/.gru/sessions/<shortID> — GRU session UUID written at launch

# Claude Code passes the hook event JSON via stdin.
HOOK_DATA=$(cat)

# Extract cwd from the hook event JSON using Python (always available).
CWD=$(python3 -c "import json,sys; print(json.load(sys.stdin).get('cwd',''))" 2>/dev/null <<< "$HOOK_DATA")
[ -n "$CWD" ] || exit 0

# The worktree path is <project>/.claude/worktrees/<shortID>.
SHORT_ID=$(basename "$CWD")
PROJECT_ROOT=$(dirname "$(dirname "$(dirname "$CWD")")")

# Look up the GRU session ID written by `gru launch`.
SESSION_FILE="$PROJECT_ROOT/.gru/sessions/$SHORT_ID"
[ -f "$SESSION_FILE" ] || exit 0
GRU_SESSION_ID=$(cat "$SESSION_FILE")
[ -n "$GRU_SESSION_ID" ] || exit 0

# Read connection config from ~/.gru/server.yaml.
# addr is "host:port" or ":port" (host-only listen); api_key is a bare value.
CONFIG="$HOME/.gru/server.yaml"
GRU_API_KEY=$(grep 'api_key' "$CONFIG" 2>/dev/null | awk '{print $2}')
ADDR=$(grep '^addr' "$CONFIG" 2>/dev/null | awk '{print $2}')
GRU_PORT="${ADDR##*:}"
GRU_HOST="${ADDR%:*}"
# If addr is ":7777", the host portion is empty — default to localhost.
GRU_HOST="${GRU_HOST:-localhost}"
GRU_PORT="${GRU_PORT:-7777}"

curl -s -m 2 -X POST \
  "http://$GRU_HOST:$GRU_PORT/events" \
  -H "Authorization: Bearer $GRU_API_KEY" \
  -H "Content-Type: application/json" \
  -H "X-Gru-Runtime: claude-code" \
  -H "X-Gru-Session-ID: $GRU_SESSION_ID" \
  -d "$HOOK_DATA" &
