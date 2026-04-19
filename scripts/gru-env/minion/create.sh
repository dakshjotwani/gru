#!/usr/bin/env bash
# create.sh — provision a minion's isolated state dir.
#
# Usage: create.sh <session-id> <mode> [parent_server_url]
#   mode = fullstack | frontend
#   parent_server_url = only consulted when mode=frontend; defaults to
#                       http://localhost:7777 (the default parent Gru).
#
# Emits on stdout, as the last non-empty line, the command-adapter contract:
#   {"provider_ref": "<state-dir>", "pty_holders": ["tmux"]}
#
# Setup logs go to stderr; anything on stdout except the last line is noise.
set -euo pipefail

SESSION_ID="${1:?missing session-id}"
MODE="${2:-fullstack}"
PARENT_URL="${3:-http://localhost:7777}"

case "$MODE" in
  fullstack|frontend) ;;
  *) echo "unknown mode: $MODE (want fullstack or frontend)" >&2; exit 2 ;;
esac

STATE_DIR="${HOME}/.gru-minions/${SESSION_ID}"

# Fail fast on collision rather than silently overwriting an existing minion.
if [[ -e "$STATE_DIR" ]]; then
  echo "state dir already exists: $STATE_DIR" >&2
  echo "  (session-id collision — rm the dir if you're sure it's stale)" >&2
  exit 3
fi

mkdir -p "$STATE_DIR/logs"

# server.yaml is created even for frontend-only mode so `make dev` has a
# consistent config to read; the SKIP_SERVER env var is what actually
# prevents the server from starting. bind: loopback keeps the minion's
# own server reachable only from the minion's host — the parent never
# talks to it.
cat > "$STATE_DIR/server.yaml" <<YAML
addr: :0
bind: loopback
db_path: ${STATE_DIR}/gru.db
YAML

# minion-env.sh is sourced by exec.sh / exec-pty.sh before every command.
# All vars here override the parent's env for anything the agent's shell
# runs — including `make dev`. NOTE: we never export GRU_HOST or GRU_PORT
# here. Those are the hook-reporting env vars the parent Gru sets on
# `claude`'s launch line so the minion's hook events flow back to the
# parent dashboard. If we overrode them the minion would vanish from
# the parent's queue.
{
  printf 'export GRU_STATE_DIR=%q\n' "$STATE_DIR"
  printf 'export GRU_SERVER_PORT=%q\n' "0"
  printf 'export GRU_WEB_PORT=%q\n'    "0"
  if [[ "$MODE" == "frontend" ]]; then
    printf 'export GRU_SKIP_SERVER=%q\n'     "1"
    printf 'export VITE_GRU_SERVER_URL=%q\n' "$PARENT_URL"
  fi
} > "$STATE_DIR/minion-env.sh"

echo "provisioned minion at ${STATE_DIR} (mode=${MODE})" >&2

# Canonical last-line JSON per the command-adapter contract.
printf '{"provider_ref": "%s", "pty_holders": ["tmux"]}\n' "$STATE_DIR"
