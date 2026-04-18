# Gru-on-Gru Parallel Minions — Design

**Date:** 2026-04-17
**Status:** Draft v1
**Scope:** A working setup where a human or a parent Gru session can spawn N minions against `~/workspace/gru` and have them edit, build, test, and dev-loop the Gru codebase in parallel with minimal conflict. Dogfoods the v2 `command` adapter.
**Companion spec:** [`2026-04-17-gru-v2-design.md`](./2026-04-17-gru-v2-design.md)

## Summary

Gru already produces git worktrees for each Claude Code session (`--worktree <shortID>`), so source-level isolation is solved. The real conflict surface is **runtime state**: port 7777 (`gru server`), port 3000 (web dev), and the single shared `~/.gru/` directory (config, SQLite db, logs, api key).

The cleanest fix, given the v2 architecture, is to express "an isolated Gru dev env" as a `command` adapter `env.yaml`, wire `LaunchSessionRequest` to accept an env spec path, and let the adapter's `create.sh` carve out per-session state directories. Dev servers bind to ephemeral ports (`:0`); `scripts/dev.sh` is taught to honor env-var overrides so both the human path (`make dev` → 7777/3000) and the minion path (`make dev` with `GRU_STATE_DIR` + `GRU_*_PORT=0`) converge on one script.

This ships two things at once: a way to run N parallel minions against the Gru repo, and a first real production user of the `command` adapter — which surfaces any ergonomics issues before a third party hits them.

## Why this shape

**What's already solved.**
- `env.Environment` + `host`/`command` adapters are shipped with the 9-case conformance suite.
- `ClaudeController.Launch` already routes through `env.Environment.Create`. The launch path is adapter-agnostic at the interface level.
- Claude Code's `--worktree <shortID>` creates per-session git worktrees under `.claude/worktrees/`. Seven are active in this checkout at time of writing.
- Go build cache, npm cache, and sqlite WAL are concurrency-safe at the filesystem level.

**What's not solved.**
- `LaunchSessionRequest` doesn't carry an env spec; `ClaudeController` holds a single `env.Environment` chosen at construction time. So per-launch adapter selection is plumbed at the interface but not exposed at the API boundary.
- `scripts/dev.sh` hardcodes `~/.gru/server.yaml`, `:7777`, and `3000`. Running two instances on one host collides on all three.
- `gru server` does not expose its bound port after `net.Listen`, so ephemeral-port dev flows can't reliably discover the URL.

**The minimal delta** is: one server flag, one dev-script env-var pass, one proto field, one controller tweak, one reusable env-spec + scripts. That stack dogfoods the `command` adapter without introducing a second launch pipeline or a new data model.

**Rejected alternatives.**
- *Static port slots per minion.* Port reservation is a TOCTOU race — no guarantee the port stays free between `lsof` and `listen`. Bind to `:0` and publish the real port after it's bound.
- *Wrapper script only (no adapter).* Works, but bypasses the `command` adapter entirely. Loses the chance to stress-test the adapter on a real project and introduces a parallel launch pipeline.
- *New `environment_specs` SQLite table.* Parked in the v2 handover as an open decision. For this design, specs live as YAML files in-repo — portable across machines, versioned with the code that consumes them.
- *Per-minion isolated `gru server` + aggregator UI.* The user's stated intent is to `ssh`/`cat` a URL from the minion, not have one dashboard. Aggregation is YAGNI.

## The pieces

### 1. `scripts/dev.sh` env-var overrides

Defaults are unchanged; existing `make dev` invocations keep working.

| Variable | Default | Meaning |
|---|---|---|
| `GRU_STATE_DIR` | `$HOME/.gru` | Where `server.yaml`, `logs/`, and the default `db_path` live. All hardcoded `$HOME/.gru/…` references in `dev.sh` route through this. |
| `GRU_SERVER_PORT` | `7777` | gRPC server port. `0` = ephemeral. |
| `GRU_WEB_PORT` | `3000` | Vite dev-server port. `0` = ephemeral. |
| `GRU_SKIP_SERVER` | unset | When `1`, skip the `gru server` step and only run the web dashboard. Used for frontend-only minions that point `VITE_GRU_SERVER_URL` at the parent's server. |
| `VITE_GRU_SERVER_URL` | derived | If preset, `dev.sh` does not overwrite it. Frontend-only minions point this at the parent's 7777. |

**Ephemeral-port publication.** When ports are `0`, `dev.sh`:
1. Launches `gru server --port-file $GRU_STATE_DIR/server.port` (server.yaml's `addr:` is `:0`).
2. Waits for `$GRU_STATE_DIR/server.port` to exist (poll with 10s timeout).
3. Launches `vite --port 0`, parses the first `Local:   http://localhost:PORT/` line from the web pipe (vite's output is stable across versions we ship against), writes the URL to `$GRU_STATE_DIR/web.port`.
4. Writes `$GRU_STATE_DIR/urls.json = {"server_url": "...", "web_url": "..."}` once both are up.
5. Prints both URLs to stdout so the minion agent sees them immediately.

**Server-yaml generation.** When `$GRU_STATE_DIR` != `$HOME/.gru`, `dev.sh` always regenerates `$GRU_STATE_DIR/server.yaml` from scratch on startup. The minion's state dir is ephemeral; no need to preserve existing config. When `$GRU_STATE_DIR == $HOME/.gru`, the existing "create if missing" behavior is preserved.

### 2. `gru server --port-file <path>`

After `net.Listen` succeeds, the server atomically writes `host:port` (e.g. `127.0.0.1:54321`) to the given path. Implementation: write to `<path>.tmp`, `os.Rename` to `<path>`. Missing path is an error; path directory is not auto-created.

### 3. `.gru/envs/minion-fullstack.yaml` + `.gru/envs/minion-frontend.yaml`

Two `command` adapter specs, both pointing at the same script set. The variant is encoded in `EnvSpecConfig.mode`, which `create.sh` reads.

```yaml
# .gru/envs/minion-fullstack.yaml
name: minion-fullstack
adapter: command
workdirs:
  - ~/workspace/gru
config:
  mode: fullstack
  create:   "scripts/gru-env/minion/create.sh {{.SessionID}} {{.EnvSpecConfig}}"
  exec:     "scripts/gru-env/minion/exec.sh {{.ProviderRef}}"
  exec_pty: "scripts/gru-env/minion/exec-pty.sh {{.ProviderRef}}"
  destroy:  "scripts/gru-env/minion/destroy.sh {{.ProviderRef}}"
  status:   "scripts/gru-env/minion/status.sh {{.ProviderRef}}"
  events:   "scripts/gru-env/minion/events.sh {{.ProviderRef}}"
```

```yaml
# .gru/envs/minion-frontend.yaml
name: minion-frontend
adapter: command
workdirs:
  - ~/workspace/gru
config:
  mode: frontend
  parent_server_url: "http://localhost:7777"
  # same scripts, same templates
```

### 4. `scripts/gru-env/minion/*.sh`

- **`create.sh <session-id> <env-spec-config-json>`** — creates `~/.gru-minions/<session-id>/`, subdirs `logs/`, writes:
  - `server.yaml` with `addr: :0`, `db_path: ~/.gru-minions/<id>/gru.db`, a freshly generated api key.
  - `minion-env.sh` — exports `GRU_STATE_DIR`, `GRU_SERVER_PORT=0`, `GRU_WEB_PORT=0`, `GRU_API_KEY=<matching-key>`. For `mode: frontend`, also exports `GRU_SKIP_SERVER=1` and `VITE_GRU_SERVER_URL=<parent_server_url>`.
  - `minion-env.sh` is idempotent (overwrite on re-run).

  Emits `{"provider_ref": "~/.gru-minions/<id>", "pty_holders": ["tmux"]}` as last stdout line.

- **`exec.sh <provider_ref> <cmd…>`** — `source "$provider_ref/minion-env.sh"`, `cd` to the first workdir (the base project dir; Claude Code's `--worktree <id>` will switch into its per-session worktree after the binary starts), exec cmd.

- **`exec-pty.sh <provider_ref> <cmd…>`** — same as `exec.sh` but wraps cmd in `script(1)` for a real controlling pty. Mirrors `test/fixtures/command-adapter/exec-pty.sh`.

- **`destroy.sh <provider_ref>`** — idempotent. If the state dir exists:
  1. Read `$provider_ref/server.pid` and `$provider_ref/web.pid` (written by `dev.sh` if the minion ran it); `kill` them, then `kill -9` after a 2s grace.
  2. `rm -rf` the state dir.

  Returning `0` whether or not the dir existed (already-gone is success).

- **`status.sh <provider_ref>`** — one-shot JSON: `{"running": <dir_exists>, "urls": <contents_of_urls.json_or_null>}`. 5s timeout from the adapter side.

- **`events.sh <provider_ref>`** — long-lived. Emits `{"kind":"heartbeat","timestamp":"..."}` every 30s. If the state dir disappears, emits `{"kind":"stopped", ...}` then exits. Respects the adapter's heartbeat contract (stall = synthesized error + respawn).

### 5. `skills/gru/gru-on-gru-minion/SKILL.md`

Rules for the minion agent, in order:

1. Your env vars (`$GRU_STATE_DIR`, `$GRU_SERVER_PORT=0`, `$GRU_WEB_PORT=0`, possibly `$GRU_SKIP_SERVER=1`) are already set by the adapter.
2. Use `make dev` as normal. It respects your env and will print your ephemeral URLs.
3. URLs are also persisted to `$GRU_STATE_DIR/urls.json` — `cat` this whenever you need to remind yourself or the operator.
4. You are in a git worktree. Your changes don't affect the parent's checkout unless merged.
5. You can `make test`, `make lint`, `make build` freely — these are worktree-local and share caches safely with other minions.
6. Do not edit `.gru/envs/` or `scripts/gru-env/minion/*` unless your task is specifically about the minion env itself. Changing them mid-run affects future minions, not yours.

### 6. `LaunchSessionRequest.env_spec_path` + controller plumbing

**Proto** (additive, backward compatible):
```proto
message LaunchSessionRequest {
  string project_dir = 1;
  string prompt = 2;
  string profile = 3;
  string name = 4;
  string description = 5;
  // ... existing fields ...
  optional string env_spec_path = 7;  // NEW. Relative to project_dir or absolute.
}
```

**Server** (`internal/server/service.go::LaunchSession`):
- If `env_spec_path` is set, resolve (relative → absolute against `project_dir`), load via existing `loadSpecFile` from `cmd/gru/cmd_env.go` (move to `internal/env/spec` so server can reuse without pulling in the CLI).
- Pass the loaded `EnvSpec` through `controller.LaunchOptions.EnvSpec`.
- On load error, return `connect.NewError(CodeInvalidArgument, ...)`.

**Controller** (`internal/controller/claude/controller.go`):
- `ClaudeController` stores `envRegistry *env.Registry` + `defaultAdapter string` ("host"), not a single `env.Environment`.
- `Launch`: if `opts.EnvSpec != nil`, resolve adapter via `envRegistry.Get(spec.Adapter)` and pass the full spec to `Create`. Otherwise construct a default spec targeting the `host` adapter with just workdirs (preserves current behavior and all existing tests).

**CLI** (`cmd/gru/cmd_launch.go`):
- `--env-spec <path>` flag. Forwards to the request.

## Data flow — full-stack minion launch

1. Parent session or CLI: `gru launch --env-spec .gru/envs/minion-fullstack.yaml --name "fix-bug-X" ~/workspace/gru "fix bug X"`.
2. Server loads the YAML, constructs `EnvSpec{Adapter: "command", Config: {mode: fullstack, create: "...", ...}}`, passes to `ClaudeController.Launch`.
3. Controller calls `envRegistry.Get("command").Create(spec)` → runs `create.sh <session-id> <config-json>` → creates `~/.gru-minions/<id>/`, writes `server.yaml`, `minion-env.sh`; returns `provider_ref = "~/.gru-minions/<id>"`.
4. Controller records the `(adapter, instance)` pair in its in-memory `live` map (keyed by sessionID), then `PersistentPty.Start` → adapter's `Exec(["tmux", "new-session", "-d", "-s", "gru-<shortID>", shellCmd])` where `shellCmd = "GRU_SESSION_ID=... GRU_API_KEY=<parent> ... claude --worktree <shortID> ..."`. **This branch does not persist `provider_ref` in SQLite** — see [Known limitations](#known-limitations) below.
5. tmux's pane runs `exec-pty.sh <provider_ref> <shellCmd>` which sources `minion-env.sh` (the exports **override** parent `GRU_API_KEY` and set `GRU_STATE_DIR` etc. — see "Env var precedence" below), then wraps in `script(1)`, then execs the shellCmd.
6. Claude Code launches inside the worktree. The agent's shell sees `$GRU_STATE_DIR`, `$GRU_SERVER_PORT=0`, `$GRU_WEB_PORT=0`. `make dev` honors them.
7. `dev.sh` boots `gru server --port-file $GRU_STATE_DIR/server.port` + `vite --port 0`, captures bound ports, writes `$GRU_STATE_DIR/urls.json`, prints URLs.
8. Agent `cat`s `urls.json` to report URLs back to the operator if asked.
9. On session kill: controller → adapter `Destroy` → `destroy.sh` kills child processes, removes state dir.

## Env var precedence

`exec-pty.sh` sources `minion-env.sh` **after** tmux has already set the parent-supplied env vars. Order:
1. tmux pane starts with parent's env (`GRU_API_KEY=<parent-key>`, `GRU_HOST`, `GRU_PORT`, etc., from `buildClaudeCmd` inlining them into `shellCmd`).
2. `exec-pty.sh` runs. It sources `minion-env.sh` which **overrides** `GRU_API_KEY` and exports `GRU_STATE_DIR`, `GRU_SERVER_PORT`, `GRU_WEB_PORT`.
3. `exec-pty.sh` then runs the `shellCmd`. Because `shellCmd` is of the form `GRU_API_KEY=<parent> ... claude ...`, the inlined `GRU_API_KEY=<parent>` on the command line **re-overrides** the env-var export back to the parent's key before `claude` starts.

**This is wrong for minions: `claude` should see the parent's hook-reporting env for hook events, but `make dev` inside the minion should see the minion's env.** Resolution:
- `claude` uses `GRU_API_KEY`, `GRU_HOST`, `GRU_PORT` to POST hook events. For a minion, hooks should go to the **parent** Gru so the parent session's queue reflects minion progress. So the parent's `GRU_API_KEY`/`GRU_HOST`/`GRU_PORT` on the `claude` invocation are **correct**.
- `make dev` inside the minion's Claude Code subshell reads a *different* set of vars: `GRU_STATE_DIR`, `GRU_SERVER_PORT`, `GRU_WEB_PORT`, `GRU_SKIP_SERVER`, `VITE_GRU_SERVER_URL`. These are set by `minion-env.sh` and never overridden by `buildClaudeCmd` (it doesn't touch them).
- So the two namespaces don't collide. `minion-env.sh` exports only the dev-env vars; `buildClaudeCmd`'s inlined vars are the hook-reporting vars; they share no keys.

**Invariant to preserve:** `minion-env.sh` MUST NOT export `GRU_API_KEY`, `GRU_HOST`, `GRU_PORT`. The minion-adapter `create.sh` will generate its own api key for its own `server.yaml`, but that key only ever reaches the minion's `gru server` child process via `server.yaml`; it is never propagated as an env var into the shell the agent runs in.

## Failure modes

| Scenario | Behavior |
|---|---|
| `create.sh` fails partway (e.g., disk full after mkdir) | Exits non-zero. Adapter calls `destroy.sh` with `provider_ref=""` per spec contract. `destroy.sh` with empty ref is a no-op. State dir may leak; operator cleans `~/.gru-minions/` manually. Documented in the skill. |
| `make dev` inside the minion dies | Minion's `gru server` + vite exit; their pidfiles go stale. `urls.json` may lie. Agent re-runs `make dev`, `dev.sh` overwrites the pidfiles and `urls.json`. |
| Parent Gru restarts mid-minion | **Not handled in this branch.** The controller only holds live adapter+instance pairs in memory, and Gru has no session-level `provider_ref` column to recover from. A restarted parent Gru cannot `gru kill` the minion cleanly — Kill falls through to the bare `tmux kill-session` branch, and `destroy.sh` is never invoked. Operator recourse: `tmux kill-session -t gru-<shortID>` + `rm -rf ~/.gru-minions/<id>` by hand. Rehydration support is a follow-up — see [Known limitations](#known-limitations). |
| Operator manually `rm -rf ~/.gru-minions/<id>` while session is live | Next `status.sh` reports `running: false`; adapter surfaces this; session transitions to `errored`. Child processes may linger if pidfiles were inside the deleted dir — operator's fault, documented. |
| Two minions accidentally point at the same `GRU_STATE_DIR` | Second one overwrites `server.yaml`, possibly corrupts sqlite. Mitigation: `create.sh` uses `<session-id>` as the dir name — session IDs are UUIDs, collision is ignorable in practice. Additionally, a short `mkdir` + `[[ -d ]]` check in `create.sh` errors on existing dir, so re-using a session id fails fast. |

## Testing

**Unit / integration tests to add:**
1. `TestDevScript_EnvVarOverrides` — shell-level test that `dev.sh` with `GRU_STATE_DIR=/tmp/foo`, `GRU_SERVER_PORT=0`, `GRU_WEB_PORT=0` writes `/tmp/foo/urls.json` with non-empty URLs.
2. `TestGruServer_PortFile` — Go test that `gru server --port-file <tmp>` with `addr: :0` writes a parseable `host:port` string to the file within 3s of startup.
3. `TestService_LaunchSession_WithEnvSpec` — server test that launching with `env_spec_path` resolves to the named adapter (fake adapter asserts it received the expected config). Existing launch tests continue to pass with unset `env_spec_path`.
4. `TestController_Launch_EnvSpec` — unit test on `ClaudeController`: with a `command`-adapter spec pointing at a test-double create script, verifies `envRegistry.Get("command")` is picked, spec flows through, and a default host-adapter path runs when `EnvSpec` is nil.
5. **Conformance:** `gru env test .gru/envs/minion-fullstack.yaml` runs the 9-case suite. Goal: 8/9 pass (rehydrate-after-kill skipped for generic runner, matching the existing `command` fixture's behavior).

**End-to-end smoke test (manual, documented in the skill):**
- With main Gru on 7777, run `gru launch --env-spec .gru/envs/minion-fullstack.yaml --name test-minion ~/workspace/gru "echo hello and run make dev"`.
- Confirm: state dir exists; agent eventually reports URLs; URLs resolve to real servers; killing the session removes the state dir.

## Implementation decomposition

Independently shippable; each commit keeps `go test ./...` green.

1. **`gru server --port-file` flag** — 10-line addition to `cmd/gru/cmd_server.go` + `internal/server`. Unit test as above.
2. **`scripts/dev.sh` env-var overrides** — refactor to read env vars, add ephemeral-port capture + `urls.json` write. Keep the default path byte-identical. Shell test as above.
3. **Launch-time env-spec selection** — proto field, `loadSpecFile` relocation to `internal/env/spec`, server + controller + CLI plumbing, tests. No behavior change when the field is unset.
4. **`.gru/envs/minion-*.yaml` + `scripts/gru-env/minion/*.sh`** — the adapter scripts + two YAML specs. Validate with `gru env test .gru/envs/minion-fullstack.yaml`.
5. **`skills/gru/gru-on-gru-minion/SKILL.md`** — operator-facing docs, minion agent guidance.
6. **`docs/workflows/gru-on-gru-minions.md`** — the user-facing "how do I spawn N minions" doc. Linked from README.

## Success criteria

This design ships when all of:

1. **Two full-stack minions + parent all run `make dev` concurrently**, each with its own state dir + ephemeral ports, no collisions.
2. **Frontend-only minion points at parent's backend** and the dashboard loads with session data from the parent's DB.
3. **`gru env test .gru/envs/minion-fullstack.yaml`** passes 8/9 conformance cases (rehydrate-after-kill skipped).
4. **Kill from the parent Gru tears down everything** — state dir gone, child processes gone, no port still bound. (Scope: so long as the parent Gru that launched the minion is still the process calling Kill — restart-survival is explicitly [a Known Limitation](#known-limitations).)
5. **The minion skill is discoverable** — `ls skills/gru/` shows it; a parent agent that reads `CLAUDE.md` and spawns a minion can follow the rules without additional prompting.

## Known limitations

These are gaps between the intended design and what actually ships in this branch. Each is deliberately deferred:

- **No Gru-restart rehydration for `command` adapter sessions.** Adapter+instance pairs are held only in `ClaudeController.live` (in-memory map). A parent Gru restart loses that map; subsequent `gru kill` for a minion session falls through to bare `tmux kill-session`, and `destroy.sh` is never called — the `~/.gru-minions/<id>/` dir and the (now orphaned) `make dev` children linger until the operator cleans them by hand. Fix requires: (1) a `provider_ref` column on `sessions`; (2) server startup code that calls `adapter.Rehydrate(provider_ref)` for every live session; (3) repopulating `ClaudeController.live` from the rehydrated pairs. None of this is urgent for the day-one dogfood path.
- **No `scaffold-env` skill for minion-on-Gru.** The existing `skills/gru/scaffold-env` covers general `command`-adapter setup. A dedicated wizard could streamline first use; not required.
- **Frontend-minion parent URL is hardcoded in the YAML spec.** `.gru/envs/minion-frontend.yaml` bakes `http://localhost:7777` into its `create:` template. A user running their parent on a non-default port needs to edit that line or swap to the fullstack variant.

## What's explicitly out of scope

- Multi-host: everything is local. `$GRU_STATE_DIR` assumes a local filesystem.
- Aggregating multiple minion Gru dashboards into one UI. Deferred. User's stated intent is to query minions individually.
- Static port reservation with `lsof` / `netstat` scans. Bind to `:0` instead.
- `environment_specs` SQLite table. Specs live as YAML files in-repo. This is deliberate — revisit if / when specs are shared across multiple projects on the same host.
- Changing the hook-event reporting flow. Minion hooks still POST to the parent Gru's `/events`, so the minion's progress appears on the parent's queue. Inverting this (minion hooks → minion Gru) would break cross-session visibility and is explicitly rejected.
