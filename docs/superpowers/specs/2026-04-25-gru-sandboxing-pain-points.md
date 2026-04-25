# Gru Sandboxing for Agent Dev — Pain Points

**Date:** 2026-04-25
**Status:** Brain dump for a future agent to fix; not a design spec.
**Context:** Trying to demo the agent-artifacts feature end-to-end on a separate test instance (different state dir, different ports) without disturbing the operator's primary `make dev`. Hit a long string of friction. Capturing it here so a follow-up agent has a complete punch list.

The two memory entries `feedback_persistent_monitor.md` and `feedback_dev_server_restart.md` already establish the rule: spin up your own dev instance, don't restart the operator's. That rule is correct. The implementation underneath it is the problem. Everything in this doc is a way the "isolated test instance" abstraction leaks.

The fixes shipped in this PR (`feat/agent-artifacts`) are bandaids that unblock the demo. Most of these issues need a real pass.

---

## What worked

`scripts/dev.sh` honors `GRU_STATE_DIR`, `GRU_SERVER_PORT`, `GRU_WEB_PORT`, `VITE_GRU_SERVER_URL`. The server side of the multi-instance story is fine — you can stand up a second `gru server` against `~/.gru-something/` on a non-default port and it doesn't collide.

The seam exists. It's everything *downstream* of the seam that doesn't honor it.

---

## What broke

### 1. CLI `defaultConfigPath()` ignored `GRU_STATE_DIR`

`cmd/gru/root.go`'s `defaultConfigPath()` was hardcoded to `$HOME/.gru/server.yaml`. A `gru artifact add` invoked from inside a test-instance-spawned session would read the *primary* server.yaml, point at port 7777, and POST to the wrong server entirely.

**Fix shipped:** route through the existing `stateDir()` helper in `server.go` which already honored the env var.

**Real fix needed:** audit every config-path resolution in `cmd/gru/` and `internal/` for hardcoded `$HOME/.gru/`. Same class of bug almost certainly hides elsewhere.

### 2. Tmux `new-session` scrubs env; controller wasn't propagating `GRU_STATE_DIR`

`internal/controller/claude/controller.go`'s launch command-line set `GRU_SESSION_ID`, `GRU_HOST`, `GRU_PORT` as `VAR=val` prefixes. It didn't pass `GRU_STATE_DIR`. Tmux's `new-session` doesn't inherit the calling process's env — it inherits the tmux server's, which is whatever shell the operator first invoked tmux from. So even after fix #1, the var wasn't there to read.

**Fix shipped:** add `GRU_STATE_DIR` to the same `VAR=val` prefix.

**Real fix needed:** generalize. The current pattern is allowlist-style: each new env var Gru wants to propagate has to be added by hand to the controller. Almost any "magic env var" pattern I added in this PR (`GRU_STATE_DIR`) or might add later (e.g. a `GRU_TEST_MODE`) hits this same wall. Spec-time env injection (or a `--inherit-env` flag, or a curated allowlist that's documented in one place) would solve the class.

### 3. Workdir-set lock is in-memory only; survives DB cleanup

`internal/env/host/host.Adapter` keeps an in-memory `activeSets` map of which workdir-sets are claimed by which instance. When I killed a session via `gru kill` (which itself failed — see #4 — so I had to clean rows from SQLite by hand), the DB was clean but the in-memory claim wasn't released. The next `LaunchSession` against the same workdir failed:

```
host: another instance is already running on the same workdir set
(held by instance "d8e63225-...")
```

The only way out was a full server restart. Mid-iteration, that costs a 10-second loop *plus* re-running every test setup step.

**Real fix needed:** persist the workdir-set claim alongside the session row, and on `KillSession` / DB delete release it. Or have the host adapter periodically reconcile against the DB. Or expose a `gru force-release-claim <workdir>` escape hatch.

### 4. `gru kill <short-id>` doesn't accept short IDs

`gru status` prints the first 8 chars of the session UUID as the user-facing handle. `gru kill <those-8-chars>` returns:

```
Error: kill session: not_found: session "d8e63225" not found
```

You have to feed it the full UUID, which means querying the DB by hand.

**Real fix needed:** prefix-match short IDs in the `KillSession` handler (and probably anywhere else that takes a session-id arg). Reject ambiguous prefixes with a clear error.

### 5. Session status stuck on "Starting…" indefinitely

After Claude Code finished its three `gru artifact add` / `gru link add` calls and printed a "Done. Summary." block, the sidebar in the dashboard still showed the session as "Starting…". The session never transitioned `starting → running → idle`. Hooks were presumably not firing, or the adapter wasn't normalizing them, or the auto-mode classifier was suppressing them — I didn't dig in. Just noting that the dashboard's status was stale relative to what the agent had actually done.

**Real fix needed:** verify the hook firing path under auto-mode Claude. Verify the status-transition logic in `internal/ingestion/handler.go` covers the events that auto-mode emits.

### 6. Trust-this-folder prompt has no programmatic bypass

A fresh Claude Code in a previously-untrusted directory blocks at "Quick safety check: Is this a project you created or one you trust? 1. Yes / 2. No". I had to `tmux send-keys -t gru-<id> "1" Enter` by hand. There's no spec field, no env var, no `--trust` flag.

For a one-off operator workflow this is fine. For agent dev where the test workdir is created by the test harness, this gates every iteration.

**Real fix needed:** either pre-trust the workdir at `gru launch` time (write to wherever Claude Code stores its trust list), or have a spec-level `pre_trust: true` flag the controller honors. Or just have `gru launch` send the "1\n" automatically when it detects the trust prompt in the pane buffer.

### 7. Test build vs production `gru` on PATH — no clean isolation

The operator's installed `gru` at `~/.local/bin/gru` is the production version. To demo the new feature without clobbering it, I copied the test build to `~/.local/bin/gru-feat` and had to literally tell the agent in its prompt: "use the binary `gru-feat`, not `gru`".

That's brittle. The agent's first instinct is to type `gru artifact add` — natural and correct given the skill text. It only worked because I told the agent to type `gru-feat`.

A proper sandbox would put the test build *first* on PATH for sessions launched from the test instance. Then the agent types `gru` and gets the test build, the operator's shell still types `gru` and gets the production build, no special prompting required.

**Real fix needed:** spec-level `path_prepend` (or controller-level injection) that prepends a test-build directory to PATH for spawned sessions. Probably wants to interact with the same env-injection mechanism #2 needs.

### 8. Project spec resolution hardcoded to `~/.gru/projects/<name>/spec.yaml`

Per the proto comment on `LaunchSessionRequest.env_spec`: *"Either an absolute path to a spec.yaml file, or a project name that resolves to ~/.gru/projects/<name>/spec.yaml."*

That `~/.gru/` is a literal `$HOME/.gru/` — not `$GRU_STATE_DIR/projects/<name>/spec.yaml`. So a test instance can't have its own named projects. Either pass absolute paths everywhere (annoying), or write specs into the operator's primary state dir (defeats isolation).

**Real fix needed:** resolve project names relative to `stateDir()` like everything else.

### 9. Skill discovery is a manual symlink

For my test agent to know about `gru artifact add` / `gru link add` via the `using-gru` skill, I had to:

```bash
mkdir -p /tmp/artifact-demo/.claude/skills
ln -s <repo>/skills/gru /tmp/artifact-demo/.claude/skills/gru
```

This is the pattern documented in `skills/README.md` so it's not new with this PR — but it's still a piece of dev-time friction. For repo-shipped skills, the controller could symlink/copy them automatically when launching against a project that's a worktree of the gru repo. Or the launcher could check `<workdir>/.claude/skills/` and auto-populate from the repo's `skills/` if missing.

### 10. Orphan tmux sessions

After `gru kill` (when it worked) and DB cleanup, the tmux session named `gru-<short-id>` was still alive on the user's tmux server. Required `tmux kill-session -t gru-<short-id>` to actually clear. Probably the same root cause as #3 — the env adapter's `Destroy` isn't getting called on every cleanup path.

### 11. No "this is the test instance" indicator in the dashboard

`http://localhost:13002/` and the operator's `http://localhost:3001/` look identical. Same "Gru" header, same layout, same colors. Easy to confuse the two when you have both open. A small banner reading the state dir / port pair from the server config and rendering it in the header would prevent a class of "wait, did I just kill my real session?" mistakes.

### 12. Setting up a real test session is many steps

To get a Claude Code session that uploads the test artifacts, I had to:

1. Build a workdir with sample files.
2. Symlink skills into `<workdir>/.claude/skills/`.
3. Write a spec.yaml.
4. Write the right prompt so the agent uses `gru-feat` not `gru`.
5. Insert a project row by hand if I'd seeded sessions earlier.
6. Launch.
7. Send "1\n" to accept the trust prompt.
8. Wait for the agent to finish.

This is too many steps to iterate on. A `gru dev fixture launch` subcommand (or a scripted equivalent in `test/fixtures/`) that does steps 1-7 from a single config file would turn it into a one-line repro.

---

## What this means for the dev loop

The pain compounds. Issue #3 means a failed launch costs a server restart. Issue #4 means recovering needs SQL. Issue #6 means every relaunch needs a manual keystroke. Issue #7 means the prompt has to be edited every time the binary changes name. By the third or fourth iteration of "tweak the implementation, restart, verify in browser," each cycle is 5+ minutes of mostly-recoverable but mostly-manual fiddling.

For most of these, a 1-day fix-pass would shave 80% off the iteration time on the next agent-side feature.

---

## Suggested order of attack for the follow-up

1. **#3 + #10 (workdir-set claim leak + orphan tmux)** — biggest blocker; one root cause; fixable in `internal/env/host/host.go` + the supervisor.
2. **#4 (short-ID matching in `gru kill`)** — half-day, immediate quality-of-life win.
3. **#6 (trust-prompt auto-accept)** — half-day; almost certainly worth a `pre_trust` flag.
4. **#7 + #2 generalization (env / PATH injection at the spec level)** — the natural mechanism for both binds them together.
5. **#1 + #8 (audit hardcoded `~/.gru/` paths)** — grep-and-replace through `cmd/` and `internal/`; small change set, broad correctness win.
6. **#11 (state-dir banner in dashboard header)** — UX, not a blocker, but cheap.
7. **#5 (status stuck on "Starting") + #9 (skill auto-symlink) + #12 (fixture launch)** — leave for a follow-up of the follow-up.
