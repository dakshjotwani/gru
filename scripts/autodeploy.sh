#!/usr/bin/env bash
# autodeploy.sh — poll origin/main, fast-forward, rebuild, restart.
#
# Runs from launchd (see deploy/launchd/com.gru.autodeploy.plist). Polls on a
# short interval. Fast-forward only — if local main has uncommitted changes or
# has diverged from origin/main, log and skip. No clobbering.
#
# Env overrides:
#   GRU_AUTODEPLOY_REPO      repo path              (default: $HOME/workspace/gru)
#   GRU_STATE_DIR            state dir              (default: $HOME/.gru)
#   GRU_AUTODEPLOY_LABEL     launchd label to kick  (default: com.gru.server)
#   GRU_AUTODEPLOY_BRANCH    tracking branch        (default: main)
#
# Flags:
#   --dry-run   log the decision + planned action; no fetch mutation, no build,
#               no restart. Useful to verify the gating logic safely.
set -euo pipefail

REPO="${GRU_AUTODEPLOY_REPO:-$HOME/workspace/gru}"
STATE_DIR="${GRU_STATE_DIR:-$HOME/.gru}"
LABEL="${GRU_AUTODEPLOY_LABEL:-com.gru.server}"
BRANCH="${GRU_AUTODEPLOY_BRANCH:-main}"
LOG_DIR="${STATE_DIR}/logs"
LOG="${LOG_DIR}/autodeploy.log"
LOCK="${STATE_DIR}/autodeploy.lock"

DRY_RUN=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    -h|--help)
      sed -n '2,20p' "$0"
      exit 0
      ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

mkdir -p "$LOG_DIR"

log() {
  printf '%s %s\n' "$(date '+%Y-%m-%dT%H:%M:%S%z')" "$*" >> "$LOG"
}

die() {
  log "ERROR: $*"
  exit 1
}

# Resolve nvm-managed node so `make build-web` (npm) is on PATH. Mirrors
# the trick in scripts/dev.sh — keeps us independent of login shell config.
if [ -f "$HOME/.nvm/alias/default" ]; then
  NVM_V="$(cat "$HOME/.nvm/alias/default")"
  NVM_BIN="$HOME/.nvm/versions/node/${NVM_V}/bin"
  if [ ! -d "$NVM_BIN" ]; then
    # "22" -> "v22.22.2"
    NVM_V="$(ls "$HOME/.nvm/versions/node/" | grep "^v${NVM_V#v}" | tail -1 || true)"
    NVM_BIN="$HOME/.nvm/versions/node/${NVM_V}/bin"
  fi
  [ -d "$NVM_BIN" ] && case ":$PATH:" in *":$NVM_BIN:"*) ;; *) export PATH="$NVM_BIN:$PATH" ;; esac
fi
# Launchd starts with a spare PATH — add homebrew + system paths for go/make/git.
case ":$PATH:" in *":/opt/homebrew/bin:"*) ;; *) export PATH="/opt/homebrew/bin:$PATH" ;; esac
case ":$PATH:" in *":/usr/bin:"*) ;; *) export PATH="$PATH:/usr/bin:/bin:/usr/sbin:/sbin" ;; esac

# Serialize: only one autodeploy runs at a time per state dir. macOS lacks a
# universally available flock/shlock — hand-rolled PID file is simplest and
# robust enough for a minute-interval poller.
if [ -e "$LOCK" ]; then
  other_pid="$(cat "$LOCK" 2>/dev/null || true)"
  if [ -n "$other_pid" ] && kill -0 "$other_pid" 2>/dev/null; then
    log "skip: another autodeploy is running (pid=$other_pid)"
    exit 0
  fi
fi
echo "$$" > "$LOCK"
trap 'rm -f "$LOCK"' EXIT

[ -e "$REPO/.git" ] || die "repo not found at $REPO (set GRU_AUTODEPLOY_REPO)"
cd "$REPO"

# Gate 1 — must be on the tracking branch.
current_branch="$(git rev-parse --abbrev-ref HEAD)"
if [ "$current_branch" != "$BRANCH" ]; then
  log "skip: current branch is '$current_branch', not '$BRANCH'"
  exit 0
fi

# Gate 2 — no uncommitted or staged changes.
if ! git diff --quiet || ! git diff --cached --quiet; then
  log "skip: working tree has uncommitted changes"
  exit 0
fi

# Gate 3 — no untracked files that would get trampled by a checkout. Rare for
# a deployment checkout, but worth surfacing.
if [ -n "$(git ls-files --others --exclude-standard)" ]; then
  log "note: untracked files present (harmless for ff-only, but noted)"
fi

# Fetch quietly. Avoid `git pull` — we want fetch + explicit ff-only merge.
local_sha="$(git rev-parse HEAD)"
if ! git fetch --quiet origin "$BRANCH"; then
  die "git fetch origin $BRANCH failed"
fi
remote_sha="$(git rev-parse "origin/$BRANCH")"

if [ "$local_sha" = "$remote_sha" ]; then
  # Nothing changed. Stay quiet in the log so it doesn't fill up with noise.
  exit 0
fi

# Gate 4 — local must be a strict ancestor of remote (fast-forward possible).
if ! git merge-base --is-ancestor "$local_sha" "$remote_sha"; then
  log "skip: local $BRANCH diverged from origin/$BRANCH (local=$local_sha remote=$remote_sha)"
  exit 0
fi

commits_behind="$(git rev-list --count "${local_sha}..${remote_sha}")"
log "update available: $commits_behind commit(s) behind origin/$BRANCH ($local_sha -> $remote_sha)"

if [ "$DRY_RUN" -eq 1 ]; then
  log "dry-run: would ff, rebuild (make build-web build), and kickstart $LABEL"
  exit 0
fi

if ! git merge --ff-only "origin/$BRANCH" >> "$LOG" 2>&1; then
  die "fast-forward merge failed (shouldn't happen after ancestor check)"
fi
log "ff-merged to $remote_sha"

log "building: make build-web build"
build_start=$(date +%s)
if ! make build-web build >> "$LOG" 2>&1; then
  log "build failed — rolling back to $local_sha"
  git reset --hard "$local_sha" >> "$LOG" 2>&1 || log "WARN: rollback reset failed"
  exit 1
fi
log "build ok in $(( $(date +%s) - build_start ))s"

# Kickstart the launchd service. `-k` stops the running instance first, then
# launchd respawns it with the fresh binary. If the service isn't loaded
# (e.g. operator hasn't migrated yet), log a warning and exit 0 — the rebuild
# still applied; next manual restart picks it up.
target="gui/$(id -u)/$LABEL"
if launchctl print "$target" >/dev/null 2>&1; then
  if launchctl kickstart -k "$target" >> "$LOG" 2>&1; then
    log "restarted $LABEL via launchctl kickstart"
  else
    log "WARN: kickstart $target returned non-zero"
  fi
else
  log "WARN: $LABEL not loaded in launchd — skip restart (operator must migrate with scripts/install-autodeploy.sh)"
fi

log "done: HEAD=$(git rev-parse --short HEAD)"
