#!/bin/bash
# Gru's residual Claude Code hook (state pipeline rev 2).
#
# The transcript-tailer architecture replaces the rev-1 multi-hook
# HTTP path. This script is the ONLY hook Gru still installs, and it
# only runs on the Notification event. Its job is exactly one thing:
# append the raw hook payload to ~/.gru/notify/<session_id>.jsonl.
#
# No network calls, no retry logic, no idempotency keys. If Gru is
# down the file simply grows; on next start the tailer catches up.
# See docs/superpowers/specs/2026-04-24-state-pipeline-design.md.

set -e

# Claude Code passes the hook event JSON via stdin.
HOOK_DATA=$(cat)

# Resolve the GRU session id from the cwd-local lookup file written
# at launch. If we can't, exit cleanly — this is informational; the
# transcript itself is still being tailed and the session won't get
# stuck.
CWD=$(printf '%s' "$HOOK_DATA" | python3 -c "import json,sys; print(json.load(sys.stdin).get('cwd',''))" 2>/dev/null || true)
[ -n "$CWD" ] || exit 0

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

# Append the payload as one JSONL line. POSIX guarantees atomic
# appends for writes <= PIPE_BUF — Gru hook payloads are well under
# that, so we don't need a lock file.
NOTIFY_DIR="$HOME/.gru/notify"
mkdir -p "$NOTIFY_DIR"
printf '%s\n' "$HOOK_DATA" >> "$NOTIFY_DIR/$GRU_SESSION_ID.jsonl"
