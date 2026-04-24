#!/usr/bin/env bash
# install-autodeploy.sh — install/uninstall the com.gru.server and
# com.gru.autodeploy LaunchAgents.
#
# Usage:
#   scripts/install-autodeploy.sh install      # copy plists, bootstrap both
#   scripts/install-autodeploy.sh uninstall    # bootout + remove plists
#   scripts/install-autodeploy.sh status       # show launchd state for both
#
# The server LaunchAgent runs <REPO>/gru directly, so the binary must exist
# (run `make serve` or `make build-web build` once before install).
#
# Before installing: stop any manually-running `gru server` process, otherwise
# launchd will fail to bind the port.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_DIR="$ROOT/deploy/launchd"
DEST_DIR="$HOME/Library/LaunchAgents"
LABELS=(com.gru.server com.gru.autodeploy)
UID_NUM="$(id -u)"

render() {
  # Expand __HOME__ → $HOME while copying.
  local src="$1" dst="$2"
  sed "s|__HOME__|${HOME}|g" "$src" > "$dst"
}

cmd_install() {
  mkdir -p "$DEST_DIR" "$HOME/.gru/logs"

  if [ ! -x "$ROOT/gru" ]; then
    echo "error: $ROOT/gru not built. Run 'make build-web build' first." >&2
    exit 1
  fi
  if ! pgrep -f "$ROOT/gru server" >/dev/null 2>&1; then
    :
  else
    echo "warning: a gru server process is already running."
    echo "         launchd bootstrap will likely fail to bind the port until you stop it."
    echo "         hit Ctrl+C to abort, or press Enter to continue."
    read -r _
  fi

  for label in "${LABELS[@]}"; do
    src="$SRC_DIR/${label}.plist"
    dst="$DEST_DIR/${label}.plist"
    [ -f "$src" ] || { echo "missing $src" >&2; exit 1; }
    render "$src" "$dst"
    echo "wrote $dst"

    target="gui/${UID_NUM}/${label}"
    if launchctl print "$target" >/dev/null 2>&1; then
      launchctl bootout "$target" 2>/dev/null || true
    fi
    launchctl bootstrap "gui/${UID_NUM}" "$dst"
    launchctl enable "$target" 2>/dev/null || true
    echo "bootstrapped $label"
  done

  echo
  echo "installed. Logs:"
  echo "  tail -f ~/.gru/logs/server.launchd.log"
  echo "  tail -f ~/.gru/logs/autodeploy.log"
}

cmd_uninstall() {
  for label in "${LABELS[@]}"; do
    target="gui/${UID_NUM}/${label}"
    if launchctl print "$target" >/dev/null 2>&1; then
      launchctl bootout "$target" || true
      echo "booted out $label"
    fi
    rm -f "$DEST_DIR/${label}.plist" && echo "removed ${label}.plist"
  done
}

cmd_status() {
  for label in "${LABELS[@]}"; do
    target="gui/${UID_NUM}/${label}"
    echo "--- $label ---"
    if launchctl print "$target" >/dev/null 2>&1; then
      launchctl print "$target" | grep -E '^\s*(state|pid|last exit code|program|run interval) ' || true
    else
      echo "not loaded"
    fi
  done
}

case "${1:-}" in
  install)   cmd_install ;;
  uninstall) cmd_uninstall ;;
  status)    cmd_status ;;
  *)
    echo "usage: $0 {install|uninstall|status}" >&2
    exit 2
    ;;
esac
