#!/usr/bin/env bash
# destroy.sh — tear down a minion's state dir and any children it spawned.
# Usage: destroy.sh <state-dir>
#
# Idempotent per the command-adapter contract: returns 0 whether or not the
# state dir existed. May be called:
#   - on normal session end (all good)
#   - after a failed create (state-dir may be ""; nothing to do)
#   - on Gru restart mid-teardown (state-dir may be half-gone)
set -euo pipefail

STATE_DIR="${1:-}"

# Empty or missing: nothing to do. Success.
if [[ -z "$STATE_DIR" || ! -d "$STATE_DIR" ]]; then
  exit 0
fi

# Try to kill any processes the minion's own `make dev` left behind. The
# server and web pid files are written by the gru server (--port-file is
# not a pid file, but we can find the PID by port) and by vite. Since we
# don't currently write pidfiles from the minion's dev stack, use pgrep
# against the state dir path — the server's cfgPath argument contains
# GRU_STATE_DIR and so does vite's cwd.
if command -v pgrep >/dev/null 2>&1; then
  # Match any process whose command line or cwd contains the state dir.
  pids="$(pgrep -f "${STATE_DIR}" 2>/dev/null || true)"
  if [[ -n "$pids" ]]; then
    # Send TERM, wait up to 2s, then KILL.
    # shellcheck disable=SC2086
    kill $pids 2>/dev/null || true
    for _ in 1 2 3 4; do
      # shellcheck disable=SC2086
      if ! kill -0 $pids 2>/dev/null; then
        break
      fi
      sleep 0.5
    done
    # shellcheck disable=SC2086
    kill -9 $pids 2>/dev/null || true
  fi
fi

rm -rf "$STATE_DIR"
