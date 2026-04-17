# Gru v2 — Environment + Queue (Personal Tool)

**Date:** 2026-04-17
**Status:** Draft v3
**Scope:** Personal tool, not a product. Runs on a local machine (or tailnet-reachable home server). Security model = single operator, trusted local network. See [Security stance](#security-stance).
**Supersedes:** `2026-04-16-gru-v2-design.md` and all earlier `2026-04-17` drafts. Prior drafts conflated env-as-environment with env-as-persistence, prescribed env formats, or carried a Playbooks pillar that didn't pay rent.

## Summary

Gru lets its operator run **many Claude Code agent sessions in parallel** against **existing local infrastructure that isn't a container** — embedded testers, lab rigs, bespoke docker-compose setups, multi-repo checkouts with custom bootstrap scripts, non-code workdirs with personal tools. That last part is what nothing else on the market handles: Sculptor and Anthropic Desktop both assume the env is a function of a git repo. Gru assumes the env already exists and agents need to be attached to it.

Gru owns two things:

1. **Environment** — adapters that wrap existing infra into a uniform lifecycle (create, exec, destroy) so sessions can be launched against heterogeneous backends without custom glue per project.
2. **Queue** — a cross-session attention surface so multiple parallel agents are supervisable, not babysittable.

Unit of work = **Session**. Projects are thin: environment + workdirs + optional brief. No recursive projects, no Decision schemas, no authority algebra, no Playbook registry, no prescribed agent roles.

Two adapters ship in v2: **`host`** (what v1 does today, retained) and **`command`** (shell-string escape hatch for arbitrary pre-existing infrastructure). `docker` / `daytona` / `e2b` are explicitly out of scope — those are product-adjacent commodities covered by existing tools.

## Why this shape

**Coder-fleet tools** (gastown, Conductor, ccswarm, Sculptor) and **Anthropic's own Desktop multi-session UI** all assume "environment = clone the repo into a sandbox." That works for self-contained OSS codebases. It fails for:

- Embedded work where the "env" includes a physical tester, JTAG pods, and a lab credential.
- Work repos with bespoke `scripts/start-dev.sh` and docker-compose setups that resist standardization.
- Multi-repo projects (kernel + uboot + buildroot, backend + infra + docs).
- Non-software work — research, finance modeling, travel planning — where the "env" is a pre-existing mix of tools, credentials, and state.

**Agent runtimes** (Claude Code) already do decomposition, sub-agents, skill discovery, notes, and escalation-by-asking when given a good brief and a stocked environment. Playbooks/skills belong in the env (`.claude/skills/`, CLAUDE.md, MCP servers); Gru does not register or own them.

**What's actually missing:**
1. A substrate-agnostic wrapper that lets existing infra be a first-class environment without rewriting it.
2. Cross-session visibility so parallel agents are supervisable.

Gru v2 does those two things.

## Two layers: environment + persistent pty

v1's tmux controller conflated two jobs. v2 separates them:

1. **Environment adapter.** Provisions a place where commands can be run. Owns lifecycle (`Create`, `Exec`, `ExecPty`, `Destroy`, `Events`, `Status`) and cross-Gru-restart rehydration (`Rehydrate`). Does not own the long-lived agent process.
2. **Persistent pty layer (Gru-owned).** Inside any environment, Gru launches the agent under a detachable pty holder (tmux), keyed by a stable name. On Gru restart, Gru reattaches by name via the adapter's `ExecPty`.

Why the split: a pty held directly by Gru dies when Gru disconnects (SIGHUP on close). The agent must live in a session multiplexer that the env keeps running. tmux-on-host was v1's implicit answer; in v2 the same trick works inside `command` adapters — tmux inside whatever the user's script provisions.

## The Environment contract

Any environment adapter must implement:

```go
type Environment interface {
    // Create a fresh instance for a session. Must be fast (<10s p50).
    Create(ctx context.Context, spec EnvSpec) (Instance, error)

    // Rehydrate an Instance from a persisted ProviderRef after a Gru restart.
    // The adapter re-locates the live backing resource (e.g. the still-running
    // tmux server on host, the still-running container, the still-existing VM)
    // and returns an Instance that behaves identically to one from Create().
    // Returns an error if the backing resource is gone.
    Rehydrate(ctx context.Context, providerRef string) (Instance, error)

    // One-shot non-interactive command.
    Exec(ctx context.Context, inst Instance, cmd []string) (ExecResult, error)

    // Interactive pty-backed command. Returns a stream Gru reads+writes as a pty.
    // Used by the PersistentPty layer to attach to tmux inside the env.
    // The stream MUST be a real pty (TIOCSCTTY-backed), not an emulated pipe.
    ExecPty(ctx context.Context, inst Instance, cmd []string) (io.ReadWriteCloser, error)

    // Tear down and release all resources. MUST be idempotent.
    Destroy(ctx context.Context, inst Instance) error

    // Lifecycle events. See Event type below.
    Events(ctx context.Context, inst Instance) (<-chan Event, error)

    // Snapshot.
    Status(ctx context.Context, inst Instance) (Status, error)
}

type EnvSpec struct {
    Name      string         // logical env name
    Adapter   string         // "host" | "command"
    Config    map[string]any // adapter-specific (command strings, flags, etc.)
    Workdirs  []string       // host paths; first = primary cwd, rest passed as --add-dir
    Resources ResourceLimits // CPU/mem/disk ceilings; advisory
}

type Instance struct {
    ID          string
    Adapter     string
    ProviderRef string   // opaque, adapter-defined, persisted by Gru, used for Rehydrate
    PtyHolders  []string // which multiplexers are available: "tmux", "dtach", ...
    StartedAt   time.Time
}

type Event struct {
    Kind      string    // "started" | "stopped" | "exit" | "oom" | "disk-full" | "error"
    Timestamp time.Time
    Detail    string    // free-form adapter-specific
}

type ResourceLimits struct {
    CPUMilli int    // 0 = unlimited
    MemMB    int    // 0 = unlimited
    DiskMB   int    // 0 = unlimited
}

type Status struct {
    Running        bool
    LastEventAt    time.Time
    DroppedEvents  int        // count since Instance start; incremented by the Events pump
    ResourceUsage  ResourceUsage
    AdapterDetail  map[string]any // free-form; host returns tmux client count, command surfaces status.sh output
}

type ResourceUsage struct {
    CPUMilli int
    MemMB    int
    DiskMB   int
}

type ExecResult struct {
    ExitCode int
    Stdout   []byte
    Stderr   []byte
}
```

### What Gru requires

- **Process persistence.** The env keeps running after Gru disconnects. `host` inherits from tmux + SQLite; `command` inherits from whatever the user's script provisions (this is the user's responsibility, documented).
- **Pty multiplexer available.** At least `tmux` installed and declared in `PtyHolders`. Gru uses it to make sessions reattachable.
- **Rehydration.** `Rehydrate(providerRef)` returns a working Instance if the backing resource is alive. Gru persists `ProviderRef` in SQLite; on startup it rehydrates active sessions before accepting new launches.
- **Idempotent teardown.** `Destroy` is safe to call multiple times.

### What Gru does NOT require

- A specific env format. Dockerfile, docker-compose, shell scripts, Makefile — all fine.
- Network topology, exposed ports, dependency management.
- Isolation (only `host` ships today, and `host` is explicitly non-isolated — see adapter notes).
- Reproducibility guarantees (the user's scripts do what they do).

## The `host` adapter

Retained from v1. Runs directly on the user's machine.

- `Create`: registers a workdir set in Gru's SQLite, no external provisioning. Creates a tmux session name.
- `Rehydrate`: checks the tmux session is still live via `tmux has-session`.
- `Exec` / `ExecPty`: forks a local shell in the workdir, optionally inside the session's tmux pane.
- `Destroy`: `tmux kill-session`.
- `Events`: file-watcher on workdirs + tmux hooks (optional).
- `Status`: tmux session state, wall-clock since last event.
- `PtyHolders`: `["tmux"]`.

**Isolation: none.** Two host sessions see the same process tree, ports, and filesystem. Documented explicitly. Host caps out ~3–5 parallel for real work due to port/cache/state contention.

## The `command` adapter

Shell-string escape hatch. User supplies commands; Gru calls them. The primary value-add of v2.

```yaml
# Example EnvSpec.Config for the command adapter
adapter: command
config:
  create: "scripts/gru-env/create.sh {{.SessionID}} {{.Workdir}}"
  exec: "scripts/gru-env/exec.sh {{.ProviderRef}}"      # argv appended
  exec_pty: "scripts/gru-env/exec-pty.sh {{.ProviderRef}}"  # argv appended
  destroy: "scripts/gru-env/destroy.sh {{.ProviderRef}}"
  events: "scripts/gru-env/events.sh {{.ProviderRef}}"  # stdout streams JSON Events
  status: "scripts/gru-env/status.sh {{.ProviderRef}}"  # stdout prints one-shot JSON Status
```

### Trust & protocol

**`create` script contract.** Runs to completion within 30s wall-clock (hard kill + `Destroy` auto-called on overrun). On success, exit 0 and emit a single JSON object as the **last non-empty line of stdout**, with optional trailing newline:

```json
{"provider_ref": "<opaque-string>", "pty_holders": ["tmux"]}
```

Everything before that last line is captured as setup log. Failure modes:

| Outcome | Gru behavior |
|---|---|
| Exit 0, last non-empty line is valid JSON with `provider_ref` (string, non-empty) | Create succeeds. `pty_holders` defaults to `["tmux"]` if missing. |
| Exit 0, missing/empty stdout | Create fails. No `Destroy` called (nothing to clean up). |
| Exit 0, last non-empty line is not valid JSON | Create fails. `Destroy` called with `provider_ref=""` as best-effort. |
| Exit 0, JSON missing `provider_ref` or `provider_ref` is empty string | Create fails. `Destroy` called with `provider_ref=""`. |
| Exit non-zero | Create fails. `Destroy` called with `provider_ref=""` if any stdout JSON parsed, else skipped. |
| Timeout (>30s) | Process killed. Create fails. `Destroy` called with `provider_ref=""`. |

**`exec` / `exec_pty` scripts.** Receive `provider_ref` as argv[1] and the agent command as argv[2..]. Responsible for "enter the env and run this." `exec_pty` MUST preserve pty semantics (allocate a controlling terminal — `script(1)`, `socat`, or `docker exec -it` all work).

**`destroy` script.** Called with `provider_ref` as argv[1]. MUST be idempotent (may be called twice if Gru restarts mid-teardown, or called after a failed Create with `provider_ref=""`). Exit 0 on success or on "already gone." Non-zero exit is logged but does not block session cleanup in Gru's state.

**`events` script.** Long-lived; each line of stdout is a JSON `Event`. Liveness contract:

- Script MUST emit `{"kind":"heartbeat"}` at least every 60 seconds of silence. Missing heartbeat for >120 seconds → Gru synthesizes `{"kind":"error","detail":"events stream stalled"}` and respawns the script.
- If script exits with any code, Gru synthesizes `{"kind":"error","detail":"events script exited with <code>"}` and waits 5s before respawning (up to 3 retries within a 5-minute window; after that the session is marked `errored`).
- On Gru restart and successful `Rehydrate`, Gru re-spawns the `events` script for each rehydrated instance (see [Attach-across-restart flow](#the-persistent-pty-layer) below).
- Backpressure: bounded channel cap=128, drop-oldest with coalescing of `started` / `stopped` / `error` / `heartbeat` (heartbeat is never retained past the next event).

**`status` script.** Prints a single JSON `Status` object to stdout and exits. Called on-demand; must return within 5s or Gru returns last-cached status with a staleness flag.

### Template variables

Rendered via Go `text/template`. `{{.Workdirs}}` and other list vars are emitted shell-escaped (each element `%q`-quoted, space-joined) so naive `$ARGS` usage in scripts is safe.

Available to every command template:
- `{{.SessionID}}`, `{{.ProjectID}}` — Gru identifiers
- `{{.Workdir}}` — first workdir (primary cwd)
- `{{.Workdirs}}` — full list, shell-escaped, space-joined
- `{{.ProviderRef}}` — from `create`'s output, available on all subsequent calls
- `{{.EnvSpecConfig}}` — full adapter config as JSON (for the user's scripts to cherry-pick)

**Secret foot-gun:** `EnvSpecConfig` is persisted in SQLite and rendered into script argv. Do NOT put tokens, passwords, or API keys in `config:`. Load them in the user's scripts from the OS keychain, 1Password CLI, sops, env vars, etc. The `scaffold-env` skill flags this at audit time.

### Responsibilities

- **User-owned:** isolation (if any), reproducibility, secret injection, resource enforcement. Gru does not attempt any of these for `command`.
- **Gru-owned:** lifecycle orchestration, `ProviderRef` persistence, `Event` / `Status` normalization, PersistentPty management on top of `ExecPty`.

## The persistent pty layer

Gru-owned. Lives above any Environment.

```go
type PersistentPty struct {}

func (p *PersistentPty) Start(ctx context.Context, env Environment, inst Instance, name string, cmd []string) error {
    // `env.Exec(inst, ["tmux", "new-session", "-d", "-s", name, cmd...])`
}

func (p *PersistentPty) Attach(ctx context.Context, env Environment, inst Instance, name string) (io.ReadWriteCloser, error) {
    // `env.ExecPty(inst, ["tmux", "attach", "-t", name])`
}

func (p *PersistentPty) Status(ctx context.Context, env Environment, inst Instance, name string) (PtyStatus, error) {
    // `env.Exec(inst, ["tmux", "has-session", "-t", name])` → exit code
}

func (p *PersistentPty) Stop(ctx context.Context, env Environment, inst Instance, name string) error {
    // `env.Exec(inst, ["tmux", "kill-session", "-t", name])`
}
```

Default backing: tmux. Gru picks the multiplexer from `Instance.PtyHolders`; if only `dtach`/`abduco` are available, the implementation switches. Only tmux is guaranteed in v2.

Attach-across-restart flow:
1. On Gru restart, load all sessions in `running` / `idle` / `needs_attention` states from SQLite.
2. For each, call `env.Rehydrate(providerRef)`. If error → mark `errored`, skip remaining steps.
3. Call `PersistentPty.Status(name=session_id)`. If missing → mark `errored`. If alive → proceed.
4. **Re-subscribe to `env.Events(inst)`** — spawn a fresh events pump that feeds the bounded channel. For the `command` adapter, this means re-spawning the long-lived `events` script.
5. UI attach calls flow through `PersistentPty.Attach`.

## Events

Env lifecycle events merge into the existing `SessionEvent` stream (proto `gru.v1.SessionEvent`) as `type="env.lifecycle"` with `payload` = JSON-encoded `Event`. The frontend's existing subscribe path handles them without a schema change beyond a new `type`.

**Backpressure:** per-session bounded channel, capacity 128. On overflow, drop oldest events except `started` / `stopped` / `error` (those are coalesced: only the latest is kept). Drop count is surfaced in `Status.DroppedEvents`.

## Project

```
Project {
  id
  name
  environment_spec_ref    // which EnvSpec this project uses by default
  workdirs                // ordered list of filesystem paths; first is primary cwd
  brief_md_ref            // optional: project-level brief in markdown
}
```

Multiple workdirs is first-class: embedded = kernel + uboot + buildroot, product = backend + infra + docs, non-code = research notes + spreadsheets + downloads. Claude Code already supports this via `--add-dir`; Gru passes the project's workdirs through. First entry = primary cwd; rest = `--add-dir`.

**Workdir semantics per adapter:**
- `host`: paths are used directly on disk. No mounting, no copying. **Gru enforces one running session per workdir set** (same ordered list of absolute paths) to avoid concurrent `npm install` / port-3000 / dirty-tree collisions. Attempting to launch a second session with an overlapping workdir set on `host` returns an error; the operator can either wait, kill the first, or use `command` adapter with per-session worktrees.
- `command`: paths are passed as template vars to the user's scripts. The script decides whether to bind-mount, copy, or symlink into its container/env. Gru does not enforce uniqueness — the user's adapter controls isolation.

No recursive sub-projects, no resources table, no milestones. A project is a place to sit with your tools.

## Session

Evolved from v1:

```
Session {
  id
  project_id                   // required
  environment_instance_ref     // Instance.ID from Environment.Create()
  brief_md                     // the goal + context
  status                       // starting | running | idle | needs_attention | ...
  attention_score              // see next section
  attention_signals            // JSON: which signals contributed to current score
  budget_dollars_used          // counter; written from Claude Code cost hook events
  budget_tokens_used           // counter; written from Claude Code token hook events
  started_at, ended_at, last_event_at
}
```

Every session belongs to one project and runs in one environment instance. Budget counters are **alert-only in v2** (no enforcement). If the Claude Code hooks don't post cost/token data, these stay zero — not a correctness failure.

## Attention score

Not UI polish. This is a real design surface — it's what determines which session the operator looks at next when 10 are running.

### v2 model (documented, hook-driven)

The existing v1 `attention_score` continues to work. v2 pins down what feeds it:

| Signal | Weight | Source |
|---|---|---|
| Paused awaiting user input (see detection rule below) | +1.0 | Derived from hook sequence |
| Agent posted a `Notification` event | +0.8 | Claude Code `Notification` hook |
| Agent hit a blocking tool error | +0.5 | Hook payload inspection (non-zero exit on `PostToolUse`) |
| Staleness: ramps linearly 0.0 → 0.3 between 5min and 15min since last hook event while `status=running` | up to +0.3 | Wall clock |
| `UserPromptSubmit` or `PreToolUse` received | snaps score to 0.0 | Hook |
| Session marked `errored` / `killed` | n/a — status wins over score | — |

**Paused-detection rule.** The `+1.0` "paused awaiting user" signal fires iff: a `Stop` hook arrived AND, looking back in this session's hook log, the most recent `UserPromptSubmit` has no matching `PostToolUse` after it (i.e., the agent never got around to tool use) OR the last tool call completed and no new output followed. Implementation: maintain a per-session "last hook" state machine; the implementer can refine the exact predicate during build — pinning the broad intent here.

**Additivity.** Weights are additive across concurrent signals (paused + staleness both active = 1.3). The `UserPromptSubmit` / `PreToolUse` snap-to-0 clears all signals; they can re-accumulate on the next event.

Score is a non-negative float (weights sum to ~2.6 max under the defaults above), persisted, recomputed on every hook event and every minute (for staleness ramp). Queue sorts `status=needs_attention` first, then by `attention_score` descending, then by `last_event_at` ascending (oldest first).

### What's deliberately NOT in v2

- LLM-based summarization of session output (expensive, not reliable enough).
- User-defined attention rules (add when v2 is being used in anger).
- Group-level attention (e.g. "this project has 3 stuck sessions"): the queue is flat.

### Tunable

Weights live in `~/.gru/server.yaml` under `attention.weights.*` so the operator can tune without a recompile. Default values above.

## The `gru:scaffold-env` skill

Claude Code skill shipped in the Gru repo at `skills/gru/scaffold-env/` (tracked in git; operators symlink it into their per-project `.claude/skills/` per the README in `skills/`). Not a Gru data model. Resolves the two operator on-ramps:

**If the user has no environment:**
1. Asks targeted questions: what's the work, tools needed, local or remote, trust level.
2. Proposes the minimum adapter (usually `host`; `command` if there's a bootstrap script).
3. Generates adapter config (for `command`: the five scripts + env spec YAML).
4. Runs a smoke test via `gru env test <name>`: `Create` → `ExecPty` a canary → `Destroy`.

**If the user has an existing env:**
1. Reads what's there (Dockerfile, `scripts/*.sh`, Makefile, etc.).
2. Audits against the contract: can it be reached via `command`? Does it support process persistence? Is tmux available?
3. Produces a cost-report (not a verdict): what parallelism it supports today, what would break at N sessions, cheapest wrapper to make it Gru-compatible.
4. Offers to generate a `command`-adapter wrapper.

### Closed decisions

- **Stance:** cost-report, not verdict. User decides what's "good enough."
- **Scope when building from scratch:** draft up to 2 files (an `env.yaml` and one create/destroy script). If it needs more, hand off to the user with notes.
- **Runtime access:** skill assumes `gru` is on PATH. No separate runtime API.
- **Long-term home:** shipped in the Gru repo for v2. Graduate to a plugin only if it outgrows the repo.

## UX surface

Three views. Existing Gru dashboard evolves; no new framework.

- **Queue (primary).** Cross-project list, sorted by `attention_score`. Row: session name, project, adapter, brief excerpt, top attention signal, time in state, actions (attach, send input, update brief, kill).
- **Projects.** Named env + workdirs + sessions; audit/edit env spec.
- **Session inspector.** Pty panel, event log, kill button (retained from v1).

## Migration from v1

Mechanical:

| v1 | v2 |
|---|---|
| Session | Gains `environment_instance_ref`, `brief_md`, `budget_*_used`, `attention_signals`. `project_id`, `attention_score` preserved. |
| Project | Gains `environment_spec_ref`, `brief_md_ref`. `path` (string) → `workdirs` ([]string). |
| tmux controller | Splits. Environment half → `host` adapter. Persistence half → default `PersistentPty` (tmux-backed), reused by both adapters. |
| Event stream | Extended with `type="env.lifecycle"`. Proto unchanged. |
| Profiles (v1) | Deleted from Gru schema. Profile content migrates into CLAUDE.md or `.claude/skills/` **inside the workdir**. In-flight sessions at migration time keep resolved profile state until they end; only new sessions need the migrated content. Migration docs ship with v2. |
| journal agent | Retained as-is; revisit integration after v2 ships. |

**Proto evolution.** All additive; no breaking changes. Web client reads new fields if present, falls back to old.

| Message | Field | Change |
|---|---|---|
| `Project` | `path` (string) | Deprecated in place. |
| `Project` | `workdirs` (repeated string) | **New.** First entry = primary cwd. |
| `Project` | `environment_spec_name` (string) | **New.** References an EnvSpec by name. |
| `Session` | `profile` (string) | Deprecated in place. |
| `Session` | `environment_instance_id` (string) | **New.** Opaque instance handle. |
| `Session` | `attention_signals` (string, JSON) | **New.** Which signals contributed to current score. |
| `Session` | `budget_dollars_used`, `budget_tokens_used` (double) | **New.** Alert-only counters. |
| `Session` | `tmux_session`, `tmux_window` | Retained for the `host` adapter; adapter-specific and may be empty on `command`. |
| `SessionEvent` | `type` values | Extended to include `"env.lifecycle"`. `payload` carries JSON-encoded `Event`. |

**EnvSpec persistence.** New SQLite table `environment_specs` (columns: `name` PK, `adapter`, `config_json`, `workdirs_json`, `resource_limits_json`, `created_at`, `updated_at`). `Project.environment_spec_name` foreign-keys to it. This keeps specs addressable by name across projects without duplicating config blobs.

Existing sessions continue on `host` unchanged. New sessions may target `command`.

## Security stance

**This is a personal tool on a local tailnet. v2 is not a multi-user or product-hardened system.**

Explicitly out of scope:
- **Secret management.** The `command` adapter runs user scripts that call whatever credential store the user already uses (1Password CLI, macOS Keychain, plain env vars). Gru does not see secrets. No `SecretRef` type.
- **Tenant isolation.** Single operator assumed.
- **Authz.** Existing bearer-token auth in v1 retained; tailnet-scoped. No per-user permissions.
- **Audit logging.** SQLite event log exists; no tamper-evidence.
- **Adapter sandbox guarantees.** `host` is explicitly non-isolated. `command` isolates as much as the user's scripts do.
- **Supply chain.** Gru trusts the user's scripts and the Claude Code binary. No signature verification.

If Gru were ever productized, every bullet above would need design work. Flagged here so it doesn't get forgotten.

## What's not in scope

Named to prevent drift:

- **No `docker` / `daytona` / `e2b` adapters.** Covered by Sculptor, Anthropic Desktop, Daytona directly. If the user needs one of those, they use that tool instead of Gru. v2 does not compete.
- **No Playbook data model.** Skills live in the env (`.claude/skills/`, CLAUDE.md, MCP). Gru contributes the `scaffold-env` skill; it does not host a registry.
- **No recursive Project model.** Sub-projects are the runtime's problem.
- **No Decision / Milestone / Resource schemas.** Escalation = `attention_score` + agent-authored notes.
- **No authority algebra.** Budgets are alert-only counters; hard-stop deferred.
- **No merge-queue equivalent.**
- **No proactive Slack/Confluence scanning.** Deferred to v3+.
- **No long-running context-building agents.** Deferred.
- **No prescribed agent hierarchy.**

## Success criteria

v2 ships when all of:

1. **Gru server restart does not kill sessions.** Kill the Gru daemon mid-session; sessions keep running in their tmux. Gru restarts, rehydrates instances, UI attach works. Demonstrable test.
2. **3 parallel agents on a real custom env in <15 minutes of wiring.** Operator picks a real work environment (target: an embedded tester or the gru-backend repo's custom docker setup), runs the `scaffold-env` skill, launches 3 parallel sessions against it. Total setup time under 15 minutes. No adapter rewrite.
3. **Queue surfaces the stuck session.** With 3+ sessions running, one hitting `needs_attention` is visibly #1 in the queue within 2 seconds (p95) of the hook event arriving.
4. **Multi-workdir works.** A session targeting a project with 3 workdirs launches Claude Code with the primary as cwd and the rest as `--add-dir`. Verified by having the agent edit files in a secondary workdir.
5. **Migration from v1 is one command.** Existing v1 sessions keep working on `host`; `Project.path` → `Project.workdirs` is automatic.
6. **Per-adapter conformance test suite.** Each adapter passes a shared contract suite. Minimum cases:
   - `Create → Destroy` (clean lifecycle)
   - `Create → Exec(echo hello) → Destroy` (one-shot exec works, returns stdout)
   - `Create → ExecPty(bash -i) → write "pwd\n" → read cwd → Destroy` (pty is real, not pipe)
   - `Create → kill-backing-resource → Rehydrate` returns error
   - `Create → (persist ProviderRef, simulate Gru restart) → Rehydrate → ExecPty works`
   - `Create → Destroy → Destroy` (idempotent — second call returns nil)
   - `Create → Events subscribe → force a lifecycle event → observe in stream → Destroy`
   - `Create → Events stream dies → observe synthesized error event → respawn → observe heartbeat`
   - `Create → Status returns running=true → Destroy → Status returns running=false or error`

   `command`-adapter tests run against an in-repo reference script set at `test/fixtures/command-adapter/`.

## Implementation decomposition

Independently shippable sub-plans, in order:

1. **Environment contract + Events schema + `PersistentPty` + `host` adapter refactor.** Extract v1's tmux controller behind the new interface. `Rehydrate` added. `Event` / `Status` structs defined. Events schema (proto `type="env.lifecycle"`, bounded-channel backpressure, heartbeat policy) lands here so later sub-plans can test against it. Conformance suite scaffolded — all cases listed in [Success criteria #6](#success-criteria) run green for `host`.
2. **`command` adapter + `gru env` CLI.** Template-based script invocation; `ProviderRef` protocol (last-line JSON + error table); destroy idempotence; events heartbeat/respawn; conformance suite green for `command`. CLI surface: `gru env list`, `gru env show <name>`, `gru env test <name>` (runs conformance suite against a user's env spec).
3. **`gru:scaffold-env` skill.** Audit + generate for `command`; assumes `gru` on PATH; ships in-repo at `.claude/skills/gru/scaffold-env/`; flags `EnvSpecConfig` secret foot-gun during audit.
4. **Queue view redesign + attention-score engine.** Attention-weight config loader (`~/.gru/server.yaml`); paused-detection state machine; staleness ramp; queue sort & per-session signal display.
5. **Multi-workdir support end-to-end.** Schema migration (`Project.path` → `Project.workdirs`); Claude Code launch wiring (`--add-dir`); host-adapter workdir-set uniqueness enforcement; UI multi-workdir picker.
6. **Gru-restart rehydration test + docs.** Demonstrable kill-daemon-restart flow in CI. Migration docs for v1→v2.

## Anti-patterns rejected

- **Prescribing an env format.** Contract over format. `command` is first-class.
- **Playbook registry inside Gru.** Skills live in the runtime.
- **Recursive projects with inherited policies.** Ceremony without validated need.
- **Hardcoded agent roles** (Mayor/Polecat). The runtime and user skills decide.
- **Policy enforcement promised in prose.** Commit to a gateway or don't claim it. v2 doesn't.
- **Platform-before-product.** Only the Environment layer is pluggable.
- **Shipping `docker`/`daytona`/`e2b`.** Commodity. Use the existing tools.
- **Pretending security is handled.** It isn't; scope is explicit.
