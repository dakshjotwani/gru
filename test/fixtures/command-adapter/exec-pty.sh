#!/usr/bin/env bash
# Reference `exec_pty` script. Uses `script(1)` to guarantee a real controlling
# tty so `stty size` and tmux attach work as expected.
#
# macOS `script` syntax differs from util-linux's. We support both by detecting
# which one is available. Falls back to just running the command if neither is
# available (tests will still pass on systems where the child doesn't strictly
# need a tty — but pty conformance will fail).
set -euo pipefail

REF="${1:?missing provider_ref}"
shift

cd "${REF}"

if [[ "$(uname -s)" == "Darwin" ]]; then
  # BSD script: `script -q /dev/null cmd ...` runs cmd under a pty.
  exec script -q /dev/null "$@"
else
  # util-linux script: `script -qefc "cmd" /dev/null` is the closest equivalent.
  cmd=""
  for arg in "$@"; do
    cmd+=$(printf ' %q' "$arg")
  done
  exec script -qefc "${cmd# }" /dev/null
fi
