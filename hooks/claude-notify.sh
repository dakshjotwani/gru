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

# Claude scrubs env vars before invoking hooks, so $GRU_SESSION_ID is
# unreliable. Resolve via cwd-local lookup files written at launch.
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

# Sibling-Claude guard: when multiple Claude processes share a cwd
# (e.g. a developer running a bare `claude` in a repo that also hosts
# a gru-launched session), every Claude's hook resolves to the same
# cwd-file and would pollute the gru session's notify file with the
# sibling's transcript_path — the tailer would then swap onto the
# wrong transcript and misreport state. Reject when stdin's session_id
# (Claude's own UUID — equals the gru session id when launched with
# --session-id) doesn't match. This trades off Notification handling
# for --resume'd sessions, where Claude mints a new UUID; that path
# is broken anyway and tracked separately as the "session.id !=
# transcript filename across --resume" ADR.
CLAUDE_SID=$(printf '%s' "$HOOK_DATA" | python3 -c "import json,sys; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null || true)
if [ -n "$CLAUDE_SID" ] && [ "$CLAUDE_SID" != "$GRU_SESSION_ID" ]; then
  exit 0
fi

# Append the payload as one JSONL line. POSIX guarantees atomic
# appends for writes <= PIPE_BUF — Gru hook payloads are well under
# that, so we don't need a lock file.
NOTIFY_DIR="$HOME/.gru/notify"
mkdir -p "$NOTIFY_DIR"
printf '%s\n' "$HOOK_DATA" >> "$NOTIFY_DIR/$GRU_SESSION_ID.jsonl"
