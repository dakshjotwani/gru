#!/usr/bin/env bash
# exec.sh — source the minion's env and run a command in it (one-shot).
# Usage: exec.sh <state-dir> <cmd> [args...]
set -euo pipefail

STATE_DIR="${1:?missing state-dir}"
shift

if [[ ! -f "$STATE_DIR/minion-env.sh" ]]; then
  echo "exec.sh: missing $STATE_DIR/minion-env.sh (was create.sh run?)" >&2
  exit 1
fi

# shellcheck source=/dev/null
source "$STATE_DIR/minion-env.sh"

exec "$@"
