#!/usr/bin/env bash
# exec.sh — source the minion's env and run a command in it (one-shot).
# Usage: exec.sh <state-dir> <cmd> [args...]
#
# Special-case: if the cmd is `tmux new-session ...`, inject -e flags for
# every minion env var so they reach the new pane. The tmux server caches
# its environment at startup — new env vars from *this* shell won't reach
# new sessions unless we explicitly pass them with -e.
set -euo pipefail

STATE_DIR="${1:?missing state-dir}"
shift

if [[ ! -f "$STATE_DIR/minion-env.sh" ]]; then
  echo "exec.sh: missing $STATE_DIR/minion-env.sh (was create.sh run?)" >&2
  exit 1
fi

# shellcheck source=/dev/null
source "$STATE_DIR/minion-env.sh"

# Collect -e KEY=VAL args for every var minion-env.sh might have set. Each
# var is optional; only set ones are injected (empty string means absent).
tmux_env_args=()
for var in GRU_STATE_DIR GRU_SERVER_PORT GRU_WEB_PORT GRU_SKIP_SERVER VITE_GRU_SERVER_URL; do
  val="${!var:-}"
  if [[ -n "$val" ]]; then
    tmux_env_args+=(-e "$var=$val")
  fi
done

# If this is `tmux new-session ...`, splice the -e flags in after `new-session`.
# The controller always invokes it that way for gru-on-gru minion launches.
if [[ "${1:-}" == "tmux" && "${2:-}" == "new-session" && ${#tmux_env_args[@]} -gt 0 ]]; then
  exec "$1" "$2" "${tmux_env_args[@]}" "${@:3}"
fi

exec "$@"
