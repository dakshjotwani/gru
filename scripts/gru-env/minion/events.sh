#!/usr/bin/env bash
# events.sh — long-lived event stream. Emits heartbeats every 30s and
# {"kind":"stopped"} when the state dir disappears.
# Usage: events.sh <state-dir>
#
# The adapter's liveness contract says heartbeats must arrive at least every
# 60s of otherwise-silent stream; 30s gives us margin.
set -euo pipefail

STATE_DIR="${1:-}"

# On a missing state dir, emit the terminal event and exit 0. The adapter
# will respawn, and we'll emit stopped again — harmless.
if [[ -z "$STATE_DIR" || ! -d "$STATE_DIR" ]]; then
  printf '{"kind":"stopped","timestamp":"%s","detail":"state dir missing"}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  exit 0
fi

# Emit an initial "started" event so subscribers know the pump is alive.
printf '{"kind":"started","timestamp":"%s","detail":"events pump up"}\n' \
  "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Heartbeat loop. The process dies cleanly on SIGTERM/SIGINT because the
# adapter closes its pipe when the instance is destroyed.
while true; do
  sleep 30
  if [[ ! -d "$STATE_DIR" ]]; then
    printf '{"kind":"stopped","timestamp":"%s","detail":"state dir removed"}\n' \
      "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    exit 0
  fi
  printf '{"kind":"heartbeat","timestamp":"%s"}\n' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
done
