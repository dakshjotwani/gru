#!/usr/bin/env bash
# destroy.sh — tear down a minion's state dir and any children it spawned.
# Usage: destroy.sh <state-dir>
#
# Idempotent per the command-adapter contract: returns 0 whether or not the
# state dir existed. May be called:
#   - on normal session end (all good — tmux kill already ended the pane
#     and with it make dev; this just rm's the state dir)
#   - after a failed create (state-dir may be ""; nothing to do)
#   - on Gru restart mid-teardown (state-dir may be half-gone)
#   - when the minion's `make dev` is running detached from the pane (rare —
#     relies on the pidfile scripts/dev.sh drops for us)
#
# Teardown order:
#   1. Read $STATE_DIR/dev.pid (written by dev.sh on startup). If the PID is
#      still alive, send SIGTERM — dev.sh's EXIT trap kills both child
#      processes and cleans up. Wait up to 3s, then SIGKILL.
#   2. rm -rf the state dir.
#
# We don't use `pgrep -f "$STATE_DIR"` here because it would match any
# unrelated process (e.g. `tail -f $STATE_DIR/logs/server.log`) a user
# might be running, and killing those is a footgun. The pidfile is
# narrower and was written by the exact process we want to stop.
set -euo pipefail

STATE_DIR="${1:-}"

if [[ -z "$STATE_DIR" || ! -d "$STATE_DIR" ]]; then
  exit 0
fi

DEV_PIDFILE="$STATE_DIR/dev.pid"
if [[ -f "$DEV_PIDFILE" ]]; then
  dev_pid="$(cat "$DEV_PIDFILE" 2>/dev/null || true)"
  if [[ -n "$dev_pid" ]] && kill -0 "$dev_pid" 2>/dev/null; then
    kill "$dev_pid" 2>/dev/null || true
    # Poll for up to 3s for the EXIT trap to clean up.
    for _ in 1 2 3 4 5 6; do
      if ! kill -0 "$dev_pid" 2>/dev/null; then
        break
      fi
      sleep 0.5
    done
    # Force-kill if still alive (its trap failed).
    if kill -0 "$dev_pid" 2>/dev/null; then
      kill -9 "$dev_pid" 2>/dev/null || true
    fi
  fi
fi

rm -rf "$STATE_DIR"
