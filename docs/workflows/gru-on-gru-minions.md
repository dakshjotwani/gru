# Running Gru minions against the Gru repo

**What this is:** a workflow for spawning N parallel Claude Code agents ("minions") to work on the Gru codebase itself, where each minion has its own isolated state (`~/.gru-minions/<id>/`), its own ephemeral ports, and its own `make dev` stack — without collision with each other or with your primary Gru instance.

**Why it matters:** the v2 `command` adapter is the mechanism that makes this possible. This workflow is also its first real production consumer, so bugs here point at real adapter-ergonomics issues.

**Status:** first-class. Two env specs ship with the repo:

- `.gru/envs/minion-fullstack.yaml` — minion runs its own `gru server` + its own web dashboard, fully isolated.
- `.gru/envs/minion-frontend.yaml` — minion runs its own web dashboard but points at your main Gru's backend on `http://localhost:7777`. Cheaper; use for frontend-only tasks.

## Prerequisites

- Your main Gru is running at `http://localhost:7777` (the usual `make dev`).
- You have `tmux` installed.
- The Gru CLI is built and on PATH (or you use the built binary directly: `/tmp/gru-dev`).

## Spawning a minion

```bash
# Full-stack: the minion gets its own server + web + db + api key.
gru launch \
  --env-spec .gru/envs/minion-fullstack.yaml \
  --name "fix-attention-staleness" \
  --description "Ramp the staleness signal at a slower rate" \
  ~/workspace/gru \
  "Find and fix the staleness weight bug per the v2 spec §Attention."

# Frontend-only: web dashboard on an ephemeral port, backed by YOUR Gru.
gru launch \
  --env-spec .gru/envs/minion-frontend.yaml \
  --name "tweak-queue-sort" \
  --description "Rework the queue sort order for tied scores" \
  ~/workspace/gru \
  "Update the queue sort so tied attention scores break by last_event_at."
```

The minion appears in your Gru dashboard as a normal session. Its `--worktree` flag (applied by `ClaudeController`) gives it an isolated git checkout under `.claude/worktrees/<shortID>/`.

## Finding the minion's URLs

The minion discovers its own ephemeral URLs once `make dev` binds and writes them to `~/.gru-minions/<id>/urls.json`. You can read them from the host side any time:

```bash
# List all live minions:
ls ~/.gru-minions/

# Get URLs for a specific minion:
cat ~/.gru-minions/<id>/urls.json
# {
#   "server_url": "http://localhost:54123",
#   "web_url":    "http://localhost:52001",
#   "state_dir":  "/Users/you/.gru-minions/<id>",
#   "started_at": "2026-04-17T16:50:12Z"
# }

# Or just ask the minion agent: "what are your urls?"
```

## What the minion sees

Inside the minion's agent process (`claude`), these env vars are already set:

- `GRU_STATE_DIR` → `~/.gru-minions/<id>`
- `GRU_SERVER_PORT=0` (ephemeral)
- `GRU_WEB_PORT=0` (ephemeral)
- `GRU_SKIP_SERVER=1` (frontend-only variant)
- `VITE_GRU_SERVER_URL=http://localhost:7777` (frontend-only variant)

The minion runs `make dev` exactly as a human would — `scripts/dev.sh` honors all of the above. Full details in `skills/gru/gru-on-gru-minion/SKILL.md` (also tracked in-repo).

## Kill / cleanup

```bash
gru kill <session-id>
```

This triggers the adapter's `destroy.sh`, which:
1. Finds any child processes still bound to the state dir (via `pgrep`).
2. Sends `SIGTERM`, waits 2s, then `SIGKILL`.
3. `rm -rf`'s `~/.gru-minions/<id>`.

Manual cleanup if something goes wrong (partial crash, leaked processes):

```bash
rm -rf ~/.gru-minions/<id>
# Then find any orphaned processes by port:
lsof -iTCP -sTCP:LISTEN -nP | grep -E '5[0-9]{4}'
```

**Heads up: if you restart the parent Gru while minions are running**, the parent loses its in-memory handle on them. `gru kill` from a restarted parent will tear down the tmux session but *not* invoke the minion adapter's `destroy.sh` — the state dir and any lingering `make dev` children stick around. Clean up manually:

```bash
tmux kill-session -t gru-<shortID>     # if still alive
rm -rf ~/.gru-minions/<id>
```

Wire-up for restart-survival (persisting `provider_ref` + rehydrating on startup) is tracked in the design spec's "Known limitations" section — it's deferred, not forgotten.

## Parallelism limits

You're bound by:
- Disk: each state dir is ~20–50MB (db + logs). 100 minions = 5GB.
- Sockets: each full-stack minion uses 2 ports. Loopback is cheap; ~10k available.
- CPU: `make dev` compiles the server + vite per minion. A 5–10 parallel ceiling is realistic on a laptop before build contention gets ugly.

For most Gru-on-Gru workflows, **3–5 parallel minions** is a comfortable practical ceiling.

## Why no static port allocation?

Because you can't reserve a port on a shared machine. Between `lsof` saying 7778 is free and your binding to 7778, anything else on the system can grab it. `:0` + published port is the only race-free approach. See the spec: `docs/superpowers/specs/2026-04-17-gru-on-gru-parallel-minions-design.md` §"Rejected alternatives."

## Debugging failures

| Symptom | First place to look |
|---|---|
| `gru launch` returns `InvalidArgument: load env spec` | Spec path typo; is `.gru/envs/minion-fullstack.yaml` reachable from the project_dir? |
| Minion session transitions to `errored` immediately | `~/.gru-minions/<id>/` doesn't exist → `create.sh` failed. Re-run `create.sh` by hand with the same session id and see what it prints on stderr. |
| `make dev` inside the minion hangs on "waiting for port file" | `gru server` crashed before binding. Look at `$GRU_STATE_DIR/logs/server.log`. |
| Minion's web dashboard loads but shows no sessions | You launched the frontend-only variant but the parent Gru isn't running on 7777. Start it, or switch to fullstack. |
| `destroy.sh` doesn't clean up a minion's processes | Processes were launched from a directory that `pgrep -f $STATE_DIR` doesn't match. Unusual — report as a bug. |

## Future work

This workflow intentionally doesn't ship with:

- A single "see all minion dashboards at once" UI. If you want it, the parent Gru already sees the minions as sessions; building an aggregator is a larger UI project.
- Automatic backend-port detection for frontend-only minions (they hardcode `http://localhost:7777`). Fine for now; change the spec's `parent_server_url` arg if you move the parent.
- Cross-machine minions. Everything is local by design.
