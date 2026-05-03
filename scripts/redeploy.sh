#!/usr/bin/env bash
# redeploy.sh — rebuild gru, install to $INSTALL_DIR, restart the supervisor service.
#
# Supports macOS (launchd kickstart) and Linux (systemctl --user restart).
# Run by hand whenever you want the supervised server to pick up new code on
# `main`. Holds a lock so concurrent invocations skip cleanly.
#
# Env overrides:
#   GRU_AUTODEPLOY_REPO     repo path             (default: $HOME/workspace/gru)
#   GRU_INSTALL_DIR         binary + frontend     (default: $HOME/.local/share/gru)
#   GRU_BIN_DIR             CLI symlink dir       (default: $HOME/.local/bin)
#   GRU_STATE_DIR           state dir             (default: $HOME/.gru)
#   GRU_LOG_DIR             log dir               (default: macOS: ~/Library/Logs/gru
#                                                            Linux: ~/.gru/logs)
#   GRU_AUTODEPLOY_LABEL    supervisor label      (default: com.gru.server)
#   GRU_AUTODEPLOY_BRANCH   tracking branch       (default: main)
set -euo pipefail

OS="$(uname -s)"

REPO="${GRU_AUTODEPLOY_REPO:-$HOME/workspace/gru}"
INSTALL_DIR="${GRU_INSTALL_DIR:-$HOME/.local/share/gru}"
BIN_DIR="${GRU_BIN_DIR:-$HOME/.local/bin}"
STATE_DIR="${GRU_STATE_DIR:-$HOME/.gru}"
LABEL="${GRU_AUTODEPLOY_LABEL:-com.gru.server}"
BRANCH="${GRU_AUTODEPLOY_BRANCH:-main}"

# Per-OS log directory default.
case "$OS" in
  Darwin) LOG_DIR="${GRU_LOG_DIR:-$HOME/Library/Logs/gru}" ;;
  *)      LOG_DIR="${GRU_LOG_DIR:-$HOME/.gru/logs}" ;;
esac

LOG="${LOG_DIR}/autodeploy.log"
LOCK="${STATE_DIR}/autodeploy.lock"
DEPLOYED_FILE="${STATE_DIR}/deployed.sha"

mkdir -p "$LOG_DIR" "$STATE_DIR"

log() {
  printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*" >> "$LOG"
}

die() {
  log "ERROR: $*"
  exit 1
}

# When invoked from a git hook running under a desktop session, PATH usually
# carries everything we need. Belt-and-suspenders for the rare case it doesn't:
# resolve nvm-managed node and (on macOS) add brew + system paths.
if [ -f "$HOME/.nvm/alias/default" ]; then
  NVM_V="$(cat "$HOME/.nvm/alias/default")"
  NVM_BIN="$HOME/.nvm/versions/node/${NVM_V}/bin"
  if [ ! -d "$NVM_BIN" ]; then
    NVM_V="$(ls "$HOME/.nvm/versions/node/" | grep "^v${NVM_V#v}" | tail -1 || true)"
    NVM_BIN="$HOME/.nvm/versions/node/${NVM_V}/bin"
  fi
  [ -d "$NVM_BIN" ] && case ":$PATH:" in *":$NVM_BIN:"*) ;; *) export PATH="$NVM_BIN:$PATH" ;; esac
fi
# Homebrew is macOS-only; skip on Linux to avoid a spurious PATH entry.
if [ "$OS" = "Darwin" ]; then
  case ":$PATH:" in *":/opt/homebrew/bin:"*) ;; *) export PATH="/opt/homebrew/bin:$PATH" ;; esac
fi
case ":$PATH:" in *":/usr/bin:"*) ;; *) export PATH="$PATH:/usr/bin:/bin:/usr/sbin:/sbin" ;; esac

# Lock — concurrent invocations skip. PID-file based; macOS lacks a portable
# flock.
if [ -e "$LOCK" ]; then
  other_pid="$(cat "$LOCK" 2>/dev/null || true)"
  if [ -n "$other_pid" ] && kill -0 "$other_pid" 2>/dev/null; then
    log "skip: redeploy already in progress (pid=$other_pid)"
    exit 0
  fi
fi
echo "$$" > "$LOCK"
trap 'rm -f "$LOCK"' EXIT

[ -e "$REPO/.git" ] || die "repo not found at $REPO (set GRU_AUTODEPLOY_REPO)"
cd "$REPO"

# Safety gates.
current_branch="$(git rev-parse --abbrev-ref HEAD)"
if [ "$current_branch" != "$BRANCH" ]; then
  log "skip: current branch '$current_branch' != '$BRANCH'"
  exit 0
fi

if ! git diff --quiet || ! git diff --cached --quiet; then
  log "skip: working tree has uncommitted changes"
  exit 0
fi

current="$(git rev-parse HEAD)"
deployed=""
[ -f "$DEPLOYED_FILE" ] && deployed="$(cat "$DEPLOYED_FILE" 2>/dev/null || true)"

if [ "$deployed" = "$current" ]; then
  # Nothing to do. Stay quiet so the log doesn't fill on no-op events.
  exit 0
fi

# Validate ancestry: deployed must be an ancestor of current. If it isn't, the
# branch was rewound or rewritten — refuse to deploy automatically.
if [ -n "$deployed" ] && ! git merge-base --is-ancestor "$deployed" "$current" 2>/dev/null; then
  log "skip: deployed=$deployed is not an ancestor of $current — manual intervention required (rm $DEPLOYED_FILE to reset)"
  exit 0
fi

if [ -n "$deployed" ]; then
  count=$(git rev-list --count "${deployed}..${current}")
  log "redeploy: $count commit(s) ahead ($deployed -> $current)"
else
  log "redeploy: initial deploy to $current"
fi

build_start=$(date +%s)
if ! make build-web build >> "$LOG" 2>&1; then
  log "build failed (no rollback — repo HEAD unchanged)"
  exit 1
fi
log "build ok in $(( $(date +%s) - build_start ))s"

# Atomic-ish binary swap: copy to a sibling then rename.
mkdir -p "$INSTALL_DIR"
install -m 755 "$REPO/gru" "$INSTALL_DIR/gru.new"
mv "$INSTALL_DIR/gru.new" "$INSTALL_DIR/gru"
log "installed binary -> $INSTALL_DIR/gru"

# Frontend mirror: stage to a sibling dir, then swap. Avoids the SPA serving
# half a build if a request lands mid-copy.
if [ -d "$REPO/web/dist" ]; then
  mkdir -p "$INSTALL_DIR/web"
  rm -rf "$INSTALL_DIR/web/dist.new"
  cp -R "$REPO/web/dist" "$INSTALL_DIR/web/dist.new"
  rm -rf "$INSTALL_DIR/web/dist.old"
  [ -d "$INSTALL_DIR/web/dist" ] && mv "$INSTALL_DIR/web/dist" "$INSTALL_DIR/web/dist.old"
  mv "$INSTALL_DIR/web/dist.new" "$INSTALL_DIR/web/dist"
  rm -rf "$INSTALL_DIR/web/dist.old"
  log "installed web/dist -> $INSTALL_DIR/web/dist"
fi

# CLI symlink (idempotent).
mkdir -p "$BIN_DIR"
ln -sfn "$INSTALL_DIR/gru" "$BIN_DIR/gru"

case "$OS" in
  Darwin)
    target="gui/$(id -u)/$LABEL"
    if launchctl print "$target" >/dev/null 2>&1; then
      if launchctl kickstart -k "$target" >> "$LOG" 2>&1; then
        log "kickstarted $LABEL"
      else
        log "WARN: kickstart $target returned non-zero"
      fi
    else
      log "WARN: $LABEL not loaded in launchd — server not restarted (run scripts/install-gru.sh install)"
    fi
    ;;
  Linux)
    if systemctl --user is-enabled gru-server.service >/dev/null 2>&1; then
      systemctl --user restart gru-server.service
      log "restarted gru-server"
    else
      log "WARN: gru-server.service not enabled — run scripts/install-gru.sh install"
    fi
    ;;
esac

echo "$current" > "$DEPLOYED_FILE"
log "done: deployed.sha=$current"
