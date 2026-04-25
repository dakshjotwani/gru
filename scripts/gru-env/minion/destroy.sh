#!/usr/bin/env bash
# destroy.sh — tear down a minion's state dir, its worktree, and any
# children it spawned.
# Usage: destroy.sh <state-dir>
#
# Idempotent per the command-adapter contract: returns 0 whether or not the
# state dir existed. May be called:
#   - on normal session end (all good — tmux kill already ended the pane
#     and with it make dev; this just rm's the state dir + worktree)
#   - after a failed create (state-dir may be ""; nothing to do)
#   - on Gru restart mid-teardown (state-dir may be half-gone)
#   - when the minion's `make dev` is running detached from the pane (rare —
#     relies on the pidfile scripts/dev.sh drops for us)
#
# Teardown order:
#   1. Read $STATE_DIR/dev.pid (written by dev.sh on startup). If the PID is
#      still alive, send SIGTERM — dev.sh's EXIT trap kills both child
#      processes and cleans up. Wait up to 3s, then SIGKILL.
#   2. Remove the per-minion git worktree (created by create.sh) and prune
#      any dangling registration. If the branch is still at its base commit
#      (i.e. the agent never committed), garbage-collect it. We deliberately
#      keep branches that have agent commits — even unmerged ones — so the
#      operator can review them before deleting.
#   3. rm -rf the state dir.
#
# We don't use `pgrep -f "$STATE_DIR"` here because it would match any
# unrelated process (e.g. `tail -f $STATE_DIR/logs/server.log`) a user
# might be running, and killing those is a footgun. The pidfile is
# narrower and was written by the exact process we want to stop.
set -euo pipefail

STATE_DIR="${1:-}"

if [[ -z "$STATE_DIR" || ! -d "$STATE_DIR" ]]; then
  exit 0
fi

DEV_PIDFILE="$STATE_DIR/dev.pid"
if [[ -f "$DEV_PIDFILE" ]]; then
  dev_pid="$(cat "$DEV_PIDFILE" 2>/dev/null || true)"
  if [[ -n "$dev_pid" ]] && kill -0 "$dev_pid" 2>/dev/null; then
    kill "$dev_pid" 2>/dev/null || true
    # Poll for up to 3s for the EXIT trap to clean up.
    for _ in 1 2 3 4 5 6; do
      if ! kill -0 "$dev_pid" 2>/dev/null; then
        break
      fi
      sleep 0.5
    done
    # Force-kill if still alive (its trap failed).
    if kill -0 "$dev_pid" 2>/dev/null; then
      kill -9 "$dev_pid" 2>/dev/null || true
    fi
  fi
fi

# Worktree cleanup. Both files are written atomically by create.sh after
# `git worktree add` succeeds, so their presence implies a real worktree.
# Their absence implies either a pre-worktree state dir or a half-finished
# create — in either case there's nothing for us to undo here.
WORKTREE_DIR="$STATE_DIR/worktree"
BRANCH_FILE="$STATE_DIR/worktree.branch"
BASE_FILE="$STATE_DIR/worktree.base"

if [[ -f "$BRANCH_FILE" ]]; then
  branch="$(cat "$BRANCH_FILE" 2>/dev/null || true)"
  base="$(cat "$BASE_FILE" 2>/dev/null || true)"

  if [[ -d "$WORKTREE_DIR" ]]; then
    # --force ignores dirty index / untracked files inside the worktree.
    git worktree remove --force "$WORKTREE_DIR" 2>/dev/null || true
  fi
  # Catch the case where someone rm-rf'd the worktree dir manually before
  # destroy ran — the registration would otherwise live on forever in
  # `git worktree list`.
  git worktree prune 2>/dev/null || true

  if [[ -n "$branch" ]] && git rev-parse --verify --quiet "refs/heads/${branch}" >/dev/null 2>&1; then
    tip="$(git rev-parse "refs/heads/${branch}" 2>/dev/null || true)"
    # If the branch tip still equals the base commit captured at create
    # time, the agent made no commits — branch carries no work, safe to
    # delete. Otherwise leave it for the operator: it may be unmerged
    # work they still want to inspect.
    if [[ -n "$base" && "$tip" == "$base" ]]; then
      git branch -D "$branch" 2>/dev/null || true
    fi
  fi
fi

rm -rf "$STATE_DIR"
