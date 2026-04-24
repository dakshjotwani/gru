#!/usr/bin/env bash
# install-gru.sh — install/uninstall the gru server LaunchAgent + git hooks.
#
# Layout after install:
#   binary:     ~/.local/share/gru/gru
#   frontend:   ~/.local/share/gru/web/dist/
#   CLI:        ~/.local/bin/gru -> ~/.local/share/gru/gru
#   server:     com.gru.server LaunchAgent (launchd-supervised, KeepAlive)
#   hooks:      core.hooksPath = scripts/git-hooks   (in this repo)
#   state:      ~/.gru/{deployed.sha,gru.db,server.yaml,...}
#   logs:       ~/Library/Logs/gru/{server.log,autodeploy.log}
#
# The git hooks rebuild and restart the server whenever local `main` moves
# (commit, pull, rebase, amend, branch checkout). No polling daemon.
#
# Usage:
#   scripts/install-gru.sh install     # build + place files + bootstrap + wire hooks
#   scripts/install-gru.sh uninstall   # bootout + remove install dir + clear hooks
#   scripts/install-gru.sh status      # current state
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLIST_SRC="$ROOT/deploy/launchd/com.gru.server.plist"
LAUNCHAGENTS_DIR="$HOME/Library/LaunchAgents"
INSTALL_DIR="${GRU_INSTALL_DIR:-$HOME/.local/share/gru}"
BIN_DIR="${GRU_BIN_DIR:-$HOME/.local/bin}"
STATE_DIR="${GRU_STATE_DIR:-$HOME/.gru}"
LOG_DIR="${GRU_LOG_DIR:-$HOME/Library/Logs/gru}"
LABEL="com.gru.server"
LEGACY_LABEL="com.gru.autodeploy"
UID_NUM="$(id -u)"

render_plist() {
  sed \
    -e "s|__HOME__|${HOME}|g" \
    -e "s|__INSTALL__|${INSTALL_DIR}|g" \
    "$PLIST_SRC"
}

bootout_if_loaded() {
  local label="$1"
  if launchctl print "gui/${UID_NUM}/${label}" >/dev/null 2>&1; then
    launchctl bootout "gui/${UID_NUM}/${label}" 2>/dev/null || true
  fi
}

cmd_install() {
  mkdir -p "$LAUNCHAGENTS_DIR" "$INSTALL_DIR" "$INSTALL_DIR/web" "$BIN_DIR" "$STATE_DIR" "$LOG_DIR"

  bootout_if_loaded "$LABEL"
  # Retire the legacy polling agent if it's still around.
  if launchctl print "gui/${UID_NUM}/${LEGACY_LABEL}" >/dev/null 2>&1; then
    launchctl bootout "gui/${UID_NUM}/${LEGACY_LABEL}" 2>/dev/null || true
    rm -f "$LAUNCHAGENTS_DIR/${LEGACY_LABEL}.plist"
    echo "removed legacy ${LEGACY_LABEL} agent"
  fi

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

  render_plist > "$LAUNCHAGENTS_DIR/${LABEL}.plist"
  echo "wrote plist       -> $LAUNCHAGENTS_DIR/${LABEL}.plist"
  launchctl bootstrap "gui/${UID_NUM}" "$LAUNCHAGENTS_DIR/${LABEL}.plist"
  launchctl enable "gui/${UID_NUM}/${LABEL}" 2>/dev/null || true
  echo "bootstrapped      -> $LABEL"

  # Wire git hooks. Setting core.hooksPath persists in the local repo's
  # .git/config — applies to this checkout (and its linked worktrees, all of
  # which share the same git common dir).
  chmod +x "$ROOT/scripts/redeploy.sh" \
           "$ROOT/scripts/git-hooks/_deploy" \
           "$ROOT/scripts/git-hooks/post-commit" \
           "$ROOT/scripts/git-hooks/post-merge" \
           "$ROOT/scripts/git-hooks/post-rewrite" \
           "$ROOT/scripts/git-hooks/post-checkout"
  git -C "$ROOT" config core.hooksPath scripts/git-hooks
  echo "wired hooks       -> core.hooksPath=scripts/git-hooks"

  # Seed deployed.sha to main HEAD (not HEAD), so installing from a feature
  # branch leaves the next switch-back-to-main as a no-op when nothing on
  # main has changed.
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
  echo "  tail -f $LOG_DIR/server.log"
  echo "  tail -f $LOG_DIR/autodeploy.log"
}

cmd_uninstall() {
  bootout_if_loaded "$LABEL"
  rm -f "$LAUNCHAGENTS_DIR/${LABEL}.plist"

  bootout_if_loaded "$LEGACY_LABEL"
  rm -f "$LAUNCHAGENTS_DIR/${LEGACY_LABEL}.plist"

  rm -f "$BIN_DIR/gru"
  rm -rf "$INSTALL_DIR"

  if git -C "$ROOT" config --get core.hooksPath >/dev/null 2>&1; then
    git -C "$ROOT" config --unset core.hooksPath || true
    echo "unset core.hooksPath"
  fi

  rm -f "$STATE_DIR/deployed.sha" "$STATE_DIR/autodeploy.lock"
  echo "uninstalled."
}

cmd_status() {
  local target="gui/${UID_NUM}/${LABEL}"
  echo "--- $LABEL ---"
  if launchctl print "$target" >/dev/null 2>&1; then
    launchctl print "$target" | grep -E '^\s*(state|pid|last exit code|program) ' || true
  else
    echo "not loaded"
  fi
  echo
  echo "--- repo state ---"
  echo "branch:         $(git -C "$ROOT" rev-parse --abbrev-ref HEAD)"
  echo "main HEAD:      $(git -C "$ROOT" rev-parse main 2>/dev/null || echo '?')"
  echo "deployed.sha:   $(cat "$STATE_DIR/deployed.sha" 2>/dev/null || echo '<unset>')"
  echo "core.hooksPath: $(git -C "$ROOT" config --get core.hooksPath || echo '<unset>')"
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
