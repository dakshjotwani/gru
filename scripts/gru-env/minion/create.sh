#!/usr/bin/env bash
# create.sh — provision a minion's isolated state dir + git worktree.
#
# Usage: create.sh <session-id> <mode> [parent_server_url]
#   mode = fullstack | frontend
#   parent_server_url = only consulted when mode=frontend; defaults to
#                       http://localhost:7777 (the default parent Gru).
#
# Each minion gets its own git worktree at $STATE_DIR/worktree on a fresh
# branch named "minion/<session-id>". Without this, concurrent minions all
# share the parent checkout and clobber each other's branches.
#
# Emits on stdout, as the last non-empty line, the command-adapter contract:
#   {"provider_ref":"<state-dir>","pty_holders":["tmux"],"workdir":"<worktree>"}
# The "workdir" field is read by the command adapter and surfaced as
# AgentArgs.Cwd, so Claude Code launches inside the worktree rather than
# the shared parent dir.
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
WORKTREE_DIR="${STATE_DIR}/worktree"
BRANCH="minion/${SESSION_ID}"

# Fail fast on collision rather than silently overwriting an existing minion.
if [[ -e "$STATE_DIR" ]]; then
  echo "state dir already exists: $STATE_DIR" >&2
  echo "  (session-id collision — rm the dir if you're sure it's stale)" >&2
  exit 3
fi

# Same fail-fast for a stale branch left over from a previous run whose
# state dir was already cleaned. Cheaper to surface here than mid-script.
if git rev-parse --verify --quiet "refs/heads/${BRANCH}" >/dev/null 2>&1; then
  echo "branch already exists: $BRANCH" >&2
  echo "  (stale from a previous run — \`git branch -D $BRANCH\` if safe)" >&2
  exit 4
fi

# If anything below this line fails, undo the partial state so create can
# be re-run with the same session id. Without this, a half-finished run
# leaves either a registered worktree, a created branch, or a populated
# state dir behind, all of which the next attempt would trip over.
cleanup_on_error() {
  local code=$?
  trap - EXIT
  [[ $code -eq 0 ]] && return 0
  if [[ -d "$WORKTREE_DIR" ]]; then
    git worktree remove --force "$WORKTREE_DIR" 2>/dev/null || true
  fi
  git worktree prune 2>/dev/null || true
  if git rev-parse --verify --quiet "refs/heads/${BRANCH}" >/dev/null 2>&1; then
    git branch -D "$BRANCH" 2>/dev/null || true
  fi
  rm -rf "$STATE_DIR"
}
trap cleanup_on_error EXIT

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

# Provision the worktree off the parent's current HEAD. The base commit
# is recorded so destroy.sh can tell whether the agent made any commits
# on this branch — branches that are still pointing at the base are safe
# to delete (nothing to lose).
BASE_COMMIT="$(git rev-parse HEAD)"
git worktree add -b "$BRANCH" "$WORKTREE_DIR" "$BASE_COMMIT" >&2
printf '%s\n' "$BASE_COMMIT" > "$STATE_DIR/worktree.base"
printf '%s\n' "$BRANCH"      > "$STATE_DIR/worktree.branch"

echo "provisioned minion at ${STATE_DIR} (mode=${MODE}, branch=${BRANCH})" >&2

# Canonical last-line JSON per the command-adapter contract.
printf '{"provider_ref":"%s","pty_holders":["tmux"],"workdir":"%s"}\n' \
  "$STATE_DIR" "$WORKTREE_DIR"
