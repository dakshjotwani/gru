#!/usr/bin/env bash
# status.sh — one-shot status report for a minion.
# Usage: status.sh <state-dir>
# Prints a single JSON object per the command-adapter Status contract.
set -euo pipefail

STATE_DIR="${1:-}"

if [[ -z "$STATE_DIR" || ! -d "$STATE_DIR" ]]; then
  echo '{"running": false}'
  exit 0
fi

URLS_FILE="$STATE_DIR/urls.json"
if [[ -f "$URLS_FILE" ]]; then
  # Inline urls.json as adapter detail so the UI can surface it later.
  urls="$(cat "$URLS_FILE")"
else
  urls="null"
fi

printf '{"running": true, "detail": {"urls": %s, "state_dir": "%s"}}\n' \
  "$urls" "$STATE_DIR"
