#!/bin/bash
# Gru hook script for Claude Code.
# Only fires when this session was launched by gru (GRU_SESSION_ID is set).
[ -n "$GRU_SESSION_ID" ] || exit 0

curl -s -m 2 -X POST \
  "http://${GRU_HOST:-localhost}:${GRU_PORT:-7777}/events" \
  -H "Authorization: Bearer ${GRU_API_KEY}" \
  -H "Content-Type: application/json" \
  -H "X-Gru-Runtime: claude-code" \
  -H "X-Gru-Session-ID: ${GRU_SESSION_ID}" \
  -d "$CLAUDE_HOOK_EVENT" &
