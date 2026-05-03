#!/usr/bin/env bash
# install-gru.sh — install/uninstall the gru server supervisor unit.
#
# Supports macOS (launchd LaunchAgent) and Linux (systemd user unit).
#
# Layout after install — macOS:
#   binary:     ~/.local/share/gru/gru
#   frontend:   ~/.local/share/gru/web/dist/
#   CLI:        ~/.local/bin/gru -> ~/.local/share/gru/gru
#   server:     com.gru.server LaunchAgent (launchd-supervised, KeepAlive)
#   state:      ~/.gru/{deployed.sha,gru.db,server.yaml,...}
#   logs:       ~/Library/Logs/gru/{server.log,autodeploy.log}
#
# Layout after install — Linux:
#   binary:     ~/.local/share/gru/gru
#   frontend:   ~/.local/share/gru/web/dist/
#   CLI:        ~/.local/bin/gru -> ~/.local/share/gru/gru
#   server:     gru-server.service (systemd user unit, Restart=always)
#   state:      ~/.gru/{deployed.sha,gru.db,server.yaml,...}
#   logs:       journald (journalctl --user -u gru-server)
#               ~/.gru/logs/autodeploy.log (redeploy.sh only)
#
# After install, run scripts/redeploy.sh by hand whenever you want the
# supervised server to pick up new code. No git-hook automation —
# the deploy is an explicit operator action.
#
# Usage:
#   scripts/install-gru.sh install     # build + place files + enable unit
#   scripts/install-gru.sh uninstall   # disable unit + remove install dir
#   scripts/install-gru.sh status      # current state
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_DIR="${GRU_INSTALL_DIR:-$HOME/.local/share/gru}"
BIN_DIR="${GRU_BIN_DIR:-$HOME/.local/bin}"
STATE_DIR="${GRU_STATE_DIR:-$HOME/.gru}"
LABEL="com.gru.server"
LEGACY_LABEL="com.gru.autodeploy"
UID_NUM="$(id -u)"

# Per-OS defaults and paths.
OS="$(uname -s)"
case "$OS" in
  Darwin)
    LOG_DIR="${GRU_LOG_DIR:-$HOME/Library/Logs/gru}"
    UNIT_SRC="$ROOT/deploy/launchd/com.gru.server.plist"
    UNIT_NAME="com.gru.server.plist"
    UNIT_DST_DIR="$HOME/Library/LaunchAgents"
    ;;
  Linux)
    LOG_DIR="${GRU_LOG_DIR:-$HOME/.gru/logs}"
    UNIT_SRC="$ROOT/deploy/systemd/gru-server.service"
    UNIT_NAME="gru-server.service"
    UNIT_DST_DIR="$HOME/.config/systemd/user"
    ;;
  *)
    echo "unsupported OS: $OS" >&2
    exit 2
    ;;
esac

# render_unit — substitute __HOME__ and __INSTALL__ placeholders in the
# supervisor unit template (works for both plist and service files).
render_unit() {
  sed \
    -e "s|__HOME__|${HOME}|g" \
    -e "s|__INSTALL__|${INSTALL_DIR}|g" \
    "$UNIT_SRC"
}

# ---- macOS helpers ----

bootout_if_loaded() {
  local label="$1"
  if launchctl print "gui/${UID_NUM}/${label}" >/dev/null 2>&1; then
    launchctl bootout "gui/${UID_NUM}/${label}" 2>/dev/null || true
  fi
}

supervisor_install_darwin() {
  bootout_if_loaded "$LABEL"
  # Retire the legacy polling agent if it's still around.
  if launchctl print "gui/${UID_NUM}/${LEGACY_LABEL}" >/dev/null 2>&1; then
    launchctl bootout "gui/${UID_NUM}/${LEGACY_LABEL}" 2>/dev/null || true
    rm -f "$UNIT_DST_DIR/${LEGACY_LABEL}.plist"
    echo "removed legacy ${LEGACY_LABEL} agent"
  fi

  render_unit > "$UNIT_DST_DIR/${UNIT_NAME}"
  echo "wrote plist       -> $UNIT_DST_DIR/${UNIT_NAME}"
  launchctl bootstrap "gui/${UID_NUM}" "$UNIT_DST_DIR/${UNIT_NAME}"
  launchctl enable "gui/${UID_NUM}/${LABEL}" 2>/dev/null || true
  echo "bootstrapped      -> $LABEL"
}

supervisor_uninstall_darwin() {
  bootout_if_loaded "$LABEL"
  rm -f "$UNIT_DST_DIR/${UNIT_NAME}"

  bootout_if_loaded "$LEGACY_LABEL"
  rm -f "$UNIT_DST_DIR/${LEGACY_LABEL}.plist"
}

supervisor_status_darwin() {
  local target="gui/${UID_NUM}/${LABEL}"
  echo "--- $LABEL ---"
  if launchctl print "$target" >/dev/null 2>&1; then
    launchctl print "$target" | grep -E '^\s*(state|pid|last exit code|program) ' || true
  else
    echo "not loaded"
  fi
}

# ---- Linux helpers ----

supervisor_install_linux() {
  # Fail fast if systemd --user is not functional. On WSL this means the user
  # needs [boot] systemd=true in /etc/wsl.conf.
  systemctl --user list-units >/dev/null 2>&1 || {
    echo "ERROR: systemctl --user not functional. On WSL, ensure /etc/wsl.conf has [boot] systemd=true and run 'wsl --shutdown'." >&2
    exit 1
  }

  mkdir -p "$UNIT_DST_DIR"
  render_unit > "$UNIT_DST_DIR/${UNIT_NAME}"
  echo "wrote unit        -> $UNIT_DST_DIR/${UNIT_NAME}"
  systemctl --user daemon-reload
  systemctl --user enable --now gru-server.service
  echo "enabled + started -> gru-server.service"

  # WSL-specific: without linger the service stops when the last shell exits.
  if grep -qiE '(microsoft|wsl)' /proc/version 2>/dev/null; then
    echo ""
    echo "WSL detected: gru will stop when your last shell exits unless you enable lingering:"
    echo "  sudo loginctl enable-linger $USER"
  fi
}

supervisor_uninstall_linux() {
  systemctl --user disable --now gru-server.service 2>/dev/null || true
  rm -f "$UNIT_DST_DIR/${UNIT_NAME}"
  systemctl --user daemon-reload
}

supervisor_status_linux() {
  echo "--- gru-server.service ---"
  systemctl --user status gru-server.service --no-pager 2>&1 | head -20 || echo "not loaded"
}

# ---- Shared commands ----

cmd_install() {
  mkdir -p "$UNIT_DST_DIR" "$INSTALL_DIR" "$INSTALL_DIR/web" "$BIN_DIR" "$STATE_DIR" "$LOG_DIR"

  echo "building gru..."
  cd "$ROOT"
  make build-web build

  install -m 755 "$ROOT/gru" "$INSTALL_DIR/gru"
  echo "installed binary  -> $INSTALL_DIR/gru"

  rm -rf "$INSTALL_DIR/web/dist"
  cp -R "$ROOT/web/dist" "$INSTALL_DIR/web/dist"
  echo "installed frontend-> $INSTALL_DIR/web/dist"

  ln -sfn "$INSTALL_DIR/gru" "$BIN_DIR/gru"
  echo "linked CLI        -> $BIN_DIR/gru"

  case "$OS" in
    Darwin) supervisor_install_darwin ;;
    Linux)  supervisor_install_linux ;;
  esac

  chmod +x "$ROOT/scripts/redeploy.sh"

  # Migrate away from the legacy git-hook autodeploy: any local repo that
  # has core.hooksPath pointing at the old scripts/git-hooks/ tree (now
  # removed) would error on every git op. Clear it on (re)install.
  if [ "$(git -C "$ROOT" config --get core.hooksPath || true)" = "scripts/git-hooks" ]; then
    git -C "$ROOT" config --unset core.hooksPath || true
    echo "removed legacy core.hooksPath"
  fi

  # Seed deployed.sha to main HEAD (not HEAD), so installing from a feature
  # branch leaves the next manual redeploy as a no-op when nothing on main
  # has changed.
  git -C "$ROOT" rev-parse main > "$STATE_DIR/deployed.sha"
  echo "seeded deployed.sha = $(cat "$STATE_DIR/deployed.sha")"

  echo
  case ":$PATH:" in
    *":$BIN_DIR:"*)
      echo "PATH includes $BIN_DIR ✓"
      ;;
    *)
      echo "WARN: $BIN_DIR is not on PATH. Add to your shell rc:"
      echo "    export PATH=\"\$HOME/.local/bin:\$PATH\""
      ;;
  esac
  echo
  echo "logs:"
  case "$OS" in
    Darwin)
      echo "  tail -f $LOG_DIR/server.log"
      echo "  tail -f $LOG_DIR/autodeploy.log"
      ;;
    Linux)
      echo "  journalctl --user -u gru-server -f"
      echo "  tail -f $LOG_DIR/autodeploy.log"
      ;;
  esac
}

cmd_uninstall() {
  case "$OS" in
    Darwin) supervisor_uninstall_darwin ;;
    Linux)  supervisor_uninstall_linux ;;
  esac

  rm -f "$BIN_DIR/gru"
  rm -rf "$INSTALL_DIR"

  # Legacy cleanup: pre-2026-04-25 installs wrote core.hooksPath = scripts/git-hooks.
  if [ "$(git -C "$ROOT" config --get core.hooksPath || true)" = "scripts/git-hooks" ]; then
    git -C "$ROOT" config --unset core.hooksPath || true
    echo "unset legacy core.hooksPath"
  fi

  rm -f "$STATE_DIR/deployed.sha" "$STATE_DIR/autodeploy.lock"
  echo "uninstalled."
}

cmd_status() {
  case "$OS" in
    Darwin) supervisor_status_darwin ;;
    Linux)  supervisor_status_linux ;;
  esac

  echo
  echo "--- repo state ---"
  echo "branch:         $(git -C "$ROOT" rev-parse --abbrev-ref HEAD)"
  echo "main HEAD:      $(git -C "$ROOT" rev-parse main 2>/dev/null || echo '?')"
  echo "deployed.sha:   $(cat "$STATE_DIR/deployed.sha" 2>/dev/null || echo '<unset>')"
  echo
  echo "--- install layout ---"
  echo "binary:    $INSTALL_DIR/gru ($([ -x "$INSTALL_DIR/gru" ] && echo present || echo MISSING))"
  echo "frontend:  $INSTALL_DIR/web/dist ($([ -d "$INSTALL_DIR/web/dist" ] && echo present || echo MISSING))"
  echo "cli link:  $BIN_DIR/gru ($(readlink "$BIN_DIR/gru" 2>/dev/null || echo '<unset>'))"
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
