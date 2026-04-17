#!/usr/bin/env bash
# exec-pty.sh — like exec.sh but wraps the command in a real pty via script(1).
# Usage: exec-pty.sh <state-dir> <cmd> [args...]
#
# Mirrors test/fixtures/command-adapter/exec-pty.sh so the minion passes the
# command-adapter pty conformance case.
set -euo pipefail

STATE_DIR="${1:?missing state-dir}"
shift

if [[ ! -f "$STATE_DIR/minion-env.sh" ]]; then
  echo "exec-pty.sh: missing $STATE_DIR/minion-env.sh (was create.sh run?)" >&2
  exit 1
fi

# shellcheck source=/dev/null
source "$STATE_DIR/minion-env.sh"

if [[ "$(uname -s)" == "Darwin" ]]; then
  # BSD script: `script -q /dev/null cmd ...` runs cmd under a pty.
  exec script -q /dev/null "$@"
else
  # util-linux script needs the command squished into one string.
  cmd=""
  for arg in "$@"; do
    cmd+=$(printf ' %q' "$arg")
  done
  exec script -qefc "${cmd# }" /dev/null
fi
