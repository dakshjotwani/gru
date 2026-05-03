#!/bin/bash
# Gru's Claude Code hook entry point (rev 3).
#
# All status-affecting hook events flow through `gru hook ingest`,
# which translates Claude's payload into gru's grammar and appends
# one line to ~/.gru/events/<sid>.jsonl. Validation, sibling-Claude
# guard, and event translation live Go-side; this script is a thin
# shim so the binary's logic is the single source of truth.
#
# Claude Code scrubs hook env, so PATH usually doesn't include
# ~/.local/bin where redeploy.sh symlinks the gru CLI. Try standard
# install locations explicitly before falling back to the search PATH.
#
# Registered hooks: see cmd/gru/init.go (hookTypes).

set -e

CANDIDATES=(
  "${GRU_BIN:-}"
  "$HOME/.local/bin/gru"
  "$HOME/.local/share/gru/gru"
  "/usr/local/bin/gru"
  "/opt/homebrew/bin/gru"
)
for c in "${CANDIDATES[@]}"; do
  if [ -n "$c" ] && [ -x "$c" ]; then
    exec "$c" hook ingest
  fi
done
# Last resort: rely on PATH (works in dev shells).
exec gru hook ingest
