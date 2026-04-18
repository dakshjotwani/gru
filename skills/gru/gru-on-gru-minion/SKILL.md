---
name: gru-on-gru-minion
description: Use this skill when you are a Gru minion spawned to work on the Gru codebase itself (via .gru/envs/minion-*.yaml). Tells you how your env is set up, what ports you have, how to run the dev stack without clobbering the parent Gru's state, and where your dashboard URL is published.
---

# Gru-on-Gru Minion Skill

You are a Claude Code agent running inside a Gru-on-Gru minion. You were launched by a parent Gru instance against the Gru repo itself; a sibling minion (or the human) may be working in parallel. This skill tells you the rules of that environment.

## Your environment

Your adapter (`command`) has already set these env vars on your shell (via `minion-env.sh` sourced before `claude` started):

| Var | Value | What it does |
|---|---|---|
| `GRU_STATE_DIR` | `~/.gru-minions/<your-session-id>` | All server state (config, logs, db) lives here — never `~/.gru/` |
| `GRU_SERVER_PORT` | `0` | Ephemeral — server binds to any free port |
| `GRU_WEB_PORT` | `0` | Ephemeral — vite binds to any free port |
| `GRU_SKIP_SERVER` | `1` (frontend-only minions only) | Skip `gru server`; use the parent's backend |
| `VITE_GRU_SERVER_URL` | `http://localhost:7777` (frontend-only minions only) | Parent Gru's backend URL |

You are in a git worktree (`--worktree <shortID>` was passed to `claude`). Your edits don't touch the parent's checkout unless you push and they merge.

## Running the dev stack

**Use `make dev` exactly as the human would.** `scripts/dev.sh` honors the env vars above, so running it starts *your* stack — not the parent's. Do not edit `dev.sh`, do not copy bits of it into a one-off script. If the dev-server flow is broken for your use case, that's a bug in `dev.sh` and fixing it there is the right call.

When the dev stack comes up, your ephemeral URLs are written to `$GRU_STATE_DIR/urls.json`:

```bash
cat "$GRU_STATE_DIR/urls.json"
# {
#   "server_url": "http://localhost:54123",
#   "web_url": "http://localhost:52001",
#   "state_dir": "/Users/you/.gru-minions/abc123",
#   "started_at": "2026-04-17T16:50:12Z"
# }
```

Report these URLs to the operator whenever they ask "where's your dashboard?" or "what ports are you on?". Also include them in status reports you write for your parent.

## Rules

1. **Never run `make dev` in a way that bypasses your env.** Things like `cd ~/workspace/gru && make dev` inherit whatever the calling shell had set. Always use `make dev` from your worktree's cwd — the env vars are already in place.
2. **Never edit `~/.gru/`, `~/.gru/server.yaml`, or anything outside `$GRU_STATE_DIR`.** That belongs to the parent.
3. **Never edit `.gru/envs/` or `scripts/gru-env/minion/`** unless your task is specifically about the minion env itself. Changes there affect future minions, not yours — future-you will thank you for not trashing them mid-flight.
4. **Hook events still flow to the parent.** `GRU_API_KEY`, `GRU_HOST`, `GRU_PORT` on your `claude` invocation point at the parent, and that's intentional — it's how you show up on the parent's queue. The minion-env.sh contract explicitly does NOT override those.
5. **Don't `rm -rf` the state dir yourself.** Your adapter's `destroy.sh` does that on kill.
6. **Use `make test`, `make lint`, `make build` freely.** These are worktree-local; the Go build cache and npm cache are concurrency-safe across minions.
7. **Kill your dev stack when you're done testing.** Leaving `gru server` + vite running in the background burns CPU; Ctrl-C the `make dev` you started.

## Verifying end-to-end

After implementing something, run it in your stack:

```bash
make build         # server binary
make test          # full Go test suite
make lint          # buf lint + go vet

# For feature verification:
make dev           # starts your isolated stack on ephemeral ports
# ... do your thing ...
# Ctrl-C to stop
```

## Discovering sibling minions

If you need to know about other running minions:

```bash
ls ~/.gru-minions/
# abc123  def456  ghi789
```

Each entry is a directory for one minion. Don't read their state without a reason — they're not your concern.

## When things go wrong

- **`make dev` says port 7777 already in use** — shouldn't happen (you have `GRU_SERVER_PORT=0`). If it does, `env | grep GRU_` to confirm your env vars are actually set. If they're not, your adapter's `exec.sh` didn't source `minion-env.sh` — report this to the operator; don't work around it.
- **`urls.json` never appears** — check `$GRU_STATE_DIR/logs/server.log` and `$GRU_STATE_DIR/logs/web.log`. One of them failed to bind.
- **You can't find the parent's API key** — you shouldn't need it. If you think you do, your task has crossed the minion/parent boundary. Ask the operator.
