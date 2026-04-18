# Env-Centric Session Launch — Design

**Date:** 2026-04-17
**Status:** Draft v1
**Scope:** Fold "where the session runs" entirely into the env spec. Drop `project_dir` and `add_dirs` from the launch API. The adapter owns source isolation (including whether to use Claude Code's `--worktree`). Gru's job shrinks to "pick an env, run an agent in it."
**Supersedes:** the project-dir-centric launch model that `2026-04-17-gru-v2-design.md` §Project and §Session still describe. Those sections are partially obsolete after this lands.
**Companion spec:** [`2026-04-17-gru-on-gru-parallel-minions-design.md`](./2026-04-17-gru-on-gru-parallel-minions-design.md) — dogfooded the command adapter, surfaced the asymmetry this spec fixes.

## Summary

Today `LaunchSessionRequest` carries `project_dir: string` and `add_dirs: []string`. The server normalizes the path, the controller unconditionally appends `--worktree <shortID>` to the claude invocation, and the env adapter gets its `Workdirs` *overridden* by the controller. The consequence: the adapter has no authority over where the session runs or how source is isolated, even though it's the thing that has to actually stand the environment up.

This spec flips the polarity. Launch becomes `{env_spec, prompt, name, description, profile}` — no paths. The env spec's `workdirs:`, adapter config, and supporting scripts decide everything about the runtime: what's visible to the agent, whether source is isolated via a worktree or a clone or a bind mount, what ports are allocated, what credentials are injected. Claude Code's `--worktree` flag stops being a global default; adapters that want it set it themselves. Adapters that don't (because the codebase is worktree-hostile, or because the adapter has its own isolation strategy) just don't.

Projects are env specs. A project row is addressed by the absolute path to its spec file. The canonical location is `~/.gru/projects/<name>/spec.yaml`, with supporting scripts and binaries living in the same directory.

**No backwards compatibility.** The `Project.path`-keyed project list, the `project_dir` launch arg, and the controller's `--worktree` injection are all removed. Existing sessions continue running until killed; the rewrite lands as a clean cut.

## Why this shape

Three current problems this fixes:

1. **`--worktree` is load-bearing for every launch**, even for codebases where it breaks tooling (absolute-path virtualenvs, docker-compose bind mounts to `/app`, non-git directories, git-LFS, fragile post-checkout hooks, IDE integrations that don't grok worktrees). The v2 design calls out the symptom ("embedded work where the env includes a physical tester, JTAG pods…") but the implementation still hardwires git worktrees as the isolation primitive. Source isolation is an adapter concern — different envs want different strategies.

2. **`spec.Workdirs` is a lie today.** The controller at `internal/controller/claude/controller.go:115` assigns `spec.Workdirs = workdirs` where `workdirs = [projectDir, addDirs...]`. Whatever the spec author wrote in their YAML gets thrown away. A `host` adapter spec that declares `workdirs: [~/repo, ~/docs]` has its carefully-ordered list silently replaced by whatever the CLI caller passed.

3. **`add_dirs` as a per-launch arg is the wrong abstraction.** "Which directories does this agent see?" is a property of the environment, not of a specific session. Two launches into the same env should get the same visibility; if you want different visibility, that's a different env. Moving `add_dirs` into the spec makes env reuse coherent.

After this change the adapter contract reads: **you are a factory for runnable environments. Given a spec, produce an instance where an agent can run. How you achieve source isolation, what paths are visible to the agent, what ports are allocated — entirely up to you.** Gru picks, Gru connects the wire, Gru doesn't second-guess.

## New API surface

### Proto

```proto
message LaunchSessionRequest {
  // Path to an env spec YAML. Absolute, or resolved against
  // ~/.gru/projects/ (so "my-proj" resolves to ~/.gru/projects/my-proj/spec.yaml).
  // Required.
  string env_spec = 1;

  string prompt      = 2;  // required
  string name        = 3;  // required, human-readable session label
  string description = 4;  // optional
  string profile     = 5;  // optional agent profile name

  // DELETED: project_dir, add_dirs, env_spec_path (renamed to env_spec above)
}
```

All proto field numbers are renumbered from scratch — this is a cut, not a migration.

### CLI

```
gru launch <env-or-dir> <prompt> --name <name> [--description <desc>] [--profile <p>]
```

First positional argument is interpreted in this order:

1. **An existing project directory** — `~/.gru/projects/<arg>/spec.yaml` exists → launch against it.
2. **A filesystem path to a spec file** — `<arg>` ends in `.yaml` and exists → launch against that file directly (ad-hoc specs outside the canonical location are allowed).
3. **A filesystem directory** (the "just here" shortcut) — `<arg>` is a directory → the CLI synthesizes a transient `host`-adapter spec targeting that dir and sends it inline. The server creates/reuses a project named `host:<slug-of-abs-path>` under `~/.gru/projects/host:<slug>/spec.yaml` so re-running the same shortcut converges.

The shortcut is pure CLI sugar; the server never sees a "bare directory" launch — it always sees an `env_spec` path.

### Project model

```
~/.gru/projects/
├── gru-minion-fullstack/
│   ├── spec.yaml            # the env spec
│   ├── scripts/
│   │   ├── create.sh
│   │   ├── exec.sh
│   │   ├── exec-pty.sh
│   │   ├── destroy.sh
│   │   ├── status.sh
│   │   └── events.sh
│   └── README.md            # optional operator docs
├── gru-minion-frontend/
│   ├── spec.yaml
│   └── scripts/...
├── my-embedded-lab/
│   ├── spec.yaml
│   ├── bin/my-flasher        # custom binaries are fine too
│   └── fixtures/
└── host:%2FUsers%2Fdak%2Fworkspace%2Fgru/   # auto-generated by "just here"
    └── spec.yaml
```

Conventions:
- Each project is a directory; the directory name *is* the project name.
- `spec.yaml` is required. Everything else is the adapter's business.
- Relative paths in the spec (`create: scripts/create.sh`, `workdirs: [../my-repo]`) are resolved against the spec file's directory. Enables fully self-contained project dirs that can be tarballed and moved between machines.
- The directory name is URL-encoded when derived from a filesystem path (so `/` becomes `%2F`). Hash alternatives discussed in "Open questions" below.

**Project identity.** `Project.id` = absolute path to `spec.yaml`. Project rows in SQLite carry:
- `id` (text, PK) — absolute path
- `name` (text) — display name (directory basename, usually)
- `adapter` (text) — cached from spec for UI listing
- `created_at`, `updated_at`

No `path` column. No `workdirs` column. No `additional_workdirs` column. The spec file is the source of truth.

### Env spec schema

```yaml
# ~/.gru/projects/<name>/spec.yaml
name: my-project                        # optional, defaults to directory name
adapter: command                        # host | command
workdirs:                               # adapter-visible paths
  - ~/workspace/my-repo                 # primary cwd for the agent
  - ~/workspace/my-repo-docs            # extra visibility (was --add-dir)
config:                                 # adapter-specific
  # for "command": the five/six shell templates, as before
  create:   "scripts/create.sh {{.SessionID}}"
  exec:     "scripts/exec.sh {{.ProviderRef}}"
  exec_pty: "scripts/exec-pty.sh {{.ProviderRef}}"
  destroy:  "scripts/destroy.sh {{.ProviderRef}}"
  status:   "scripts/status.sh {{.ProviderRef}}"
  events:   "scripts/events.sh {{.ProviderRef}}"
```

For `host` adapter:

```yaml
name: my-quick-project
adapter: host
workdirs:
  - ~/workspace/my-repo
config:
  worktree: false                       # NEW. See below.
```

## Host-adapter changes

### `worktree` config knob (default OFF)

Today `ClaudeController` unconditionally passes `--worktree <shortID>` to claude. After this change, that lives behind a spec-declared knob:

```yaml
adapter: host
config:
  worktree: true        # opt in per spec
```

- **Default: off.** Worktrees are an opinion, not a universal good. Users who want them say so.
- `host` adapter's `Create` reads `config.worktree`. When true, it reserves a worktree short-id and appends `--worktree <shortID>` to the claude argv via a new plumbing point (see "Agent args" below). When false, the agent runs against `workdirs[0]` directly.
- The existing workdir-set uniqueness enforcement becomes conditional: when `worktree: false` two host sessions with overlapping workdirs still collide on ports/files, so uniqueness enforcement stays. When `worktree: true` each session gets its own worktree path and the uniqueness check is moot. Implementation: one host-adapter invariant "same workdirs + `worktree: false` = collision" regardless of which session asked first.

### Agent args — adapter → controller seam

The controller builds the claude argv today. After this change, it asks the adapter: "given this instance, what flags should I append before the prompt?" `env.Environment` grows:

```go
type AgentArgs struct {
    // Appended to the claude invocation after the base flags but before
    // the prompt. Adapters use this to declare worktree, add-dir, etc.
    ExtraArgs []string

    // Working directory the claude process should be launched in. When
    // empty, controller uses Workdirs[0]. Adapters that set up an
    // isolated source tree (shallow clone, bind mount, etc.) return
    // that path here.
    Cwd string
}

type Environment interface {
    // ... existing methods ...

    // AgentArgs is called after Create() to discover any per-launch flags
    // and the cwd the agent process should run in.
    AgentArgs(ctx context.Context, inst Instance) (AgentArgs, error)
}
```

Returning `AgentArgs{}` (zero value) means "launch in `Workdirs[0]`, no extra args" — the minimal behavior.

- `host` adapter with `worktree: true`: returns `{ExtraArgs: ["--worktree", shortID], Cwd: ""}`.
- `host` adapter with `worktree: false`: returns `{}`.
- `command` adapter: returns `{}` by default; a specific user script could surface an alternate cwd (e.g., create.sh's JSON output grows an optional `workdir` field that the adapter plumbs through).

## Command adapter

No changes to the scripting contract. `create.sh` still returns `{"provider_ref": "...", "pty_holders": [...]}`. The spec's `workdirs:` is what the adapter passes to every template. `add_dirs` doesn't exist in the launch API anymore, so the template-variable `{{.Workdirs}}` now faithfully reflects what the spec declared.

Optional additive tweak: `create.sh` may return a `workdir` field in its JSON:
```json
{"provider_ref": "...", "pty_holders": ["tmux"], "workdir": "/tmp/fresh-clone/src"}
```
The command adapter's Go code decodes this and surfaces it via `AgentArgs.Cwd`. Enables "I did a shallow clone in create.sh, the agent should run in the clone dir, not `Workdirs[0]`." Optional field — scripts that don't emit it continue to work.

## Web UI

- **Project picker** lists every `~/.gru/projects/*/spec.yaml`. Row shows project name + adapter + first workdir (as a friendly label).
- **Launch modal** drops the path picker entirely. User picks a project and types a prompt.
- **"Just here" launch is CLI-only.** The web UI always works against named, persisted projects. Keeps the web flow clean: a launch from the UI is always reproducible via the same spec.

## Migration

No backwards compat. This ships as a clean cut. Operational steps:

1. Delete old `Project.path`, `Session.add_dirs`, any proto field previously removed.
2. Delete `.gru/envs/` and `scripts/gru-env/` from the repo — their content moves to `~/.gru/projects/gru-minion-fullstack/` and `~/.gru/projects/gru-minion-frontend/` (or wherever the operator wants). Ship a one-off script (`scripts/install-minion-projects.sh`) that populates these from in-repo templates as a convenience.
3. Any existing session in `running`/`idle`/`needs_attention` is orphaned: the new controller has no in-memory handle for it, no way to rehydrate, and the Session's `project_id` points at a string that's no longer a valid path. Operator clears them by hand (`gru prune --all` gets extended to cover this). One-shot.
4. Existing `tmux` sessions keep running (tmux doesn't care). Operator kills them manually via `tmux kill-session` as they finish.

For a personal tool this is acceptable. Productized, it would need a real migration path.

## Implementation decomposition

Each step keeps `go test ./...` green independently.

1. **`env.Environment.AgentArgs` + controller wiring.** New interface method; `host` and `command` adapters get default implementations returning `AgentArgs{}`. Controller consults AgentArgs to build the claude invocation; `--worktree` injection moves out of `buildClaudeCmd` into the host adapter's AgentArgs response (gated by `config.worktree`).

2. **Host adapter `worktree: bool` config + default-off behavior.** Adds the knob, plumbs it. Defaults existing calls to false. **This is the breaking change moment** — every existing spec that relied on implicit worktrees stops getting them.

3. **Proto rewrite.** `LaunchSessionRequest` → new shape. Regenerate. The `LaunchSession` server handler is rewritten to accept `env_spec` path, load via `internal/env/spec.LoadFile`, compute project id from absolute path, upsert project row. Kill `add_dirs` plumbing throughout (server, controller, CLI, web).

4. **Project schema rewrite.** SQLite migration: `projects` table gets `id` (abs path) as PK, drops `path` and `additional_workdirs` columns. `sessions.project_id` continues to reference it but now contains a spec path instead of a repo path. Ship `scripts/clean-old-projects.sql` for operators to run post-upgrade.

5. **CLI shortcut logic.** `gru launch <first-arg> <prompt>` dispatches between project-name / spec-file / bare-directory per §CLI. Bare-directory path synthesizes a host-adapter spec at `~/.gru/projects/host:<slug>/spec.yaml`, then resolves through the same path as every other launch.

6. **Install script for gru-on-gru minion projects.** `scripts/install-minion-projects.sh` copies `~/workspace/gru/.gru/envs/minion-*.yaml` (still in the repo as templates) and `scripts/gru-env/minion/` into `~/.gru/projects/gru-minion-{fullstack,frontend}/`, rewriting the template's relative paths to match the new location. Run once; operator can edit freely afterward.

7. **Web UI updates.** Project list reads `~/.gru/projects/`. Launch modal drops the path picker. Session rows display the project's name + adapter instead of a path.

## Success criteria

1. **`gru launch` never takes a `project_dir`.** Proto, CLI, server, controller all consistent.
2. **Host-adapter worktree defaults to off.** A freshly-installed Gru running `gru launch ~/my-repo "hi"` (just-here shortcut) runs claude in `~/my-repo` directly, no `.claude/worktrees/` directory created. Opt-in via `config.worktree: true` in the generated spec.
3. **Spec's `workdirs:` is authoritative.** Setting `workdirs: [~/a, ~/b, ~/c]` in the YAML and launching produces a claude invocation with `~/a` as cwd and `--add-dir ~/b --add-dir ~/c` — regardless of what the CLI caller passed. (Not that they pass anything after this change.)
4. **`~/.gru/projects/` is the project registry.** Every project visible in `gru projects list` corresponds to a `~/.gru/projects/*/spec.yaml`. No exceptions, no orphan rows.
5. **The just-here shortcut is idempotent.** `gru launch ~/foo "task 1"` then `gru launch ~/foo "task 2"` produces one project row (two sessions), not two project rows.
6. **Gru-on-gru minions still work via the canonical flow.** `gru launch gru-minion-fullstack "go do X"` succeeds, provisions `~/.gru-minions/<id>/`, passes conformance. The only change: the spec lives under `~/.gru/projects/gru-minion-fullstack/` instead of `~/workspace/gru/.gru/envs/`.

## Open questions

- **Auto-generated project names.** `host:<url-encoded-abs-path>` is ugly (`host:%2FUsers%2Fdak%2Fworkspace%2Fgru`). Alternatives: hash-based (`host-a1b2c3d4`), basename-with-tiebreaker (`gru`, `gru-2`, …), or just the basename (`gru`) with collision rejection. Hash is deterministic and uniqueness-free; basename is human-friendly but racy. My lean: basename + collision-tiebreak (`gru`, `gru-2`). Operator-changeable via `mv ~/.gru/projects/gru-2 ~/.gru/projects/my-gru-fork`.
- **Spec validation.** Should `LoadFile` reject unknown adapter names up front, or defer to the controller? Up-front is friendlier (fail at `gru launch` time with a clear message rather than deep inside Create). Adds a coupling between the loader package and the registry.
- **Inline specs in `LaunchSessionRequest`.** For the CLI shortcut, the server currently resolves `env_spec = <spec path>`. Could also carry an inline `env_spec_content` for truly ephemeral "don't persist this" launches. Probably not needed; the canonical location + slug-based caching covers the use case.

## Out of scope

- **A general "project registry" service.** `~/.gru/projects/` is a filesystem convention, not a managed API. Third-party tools read the directory; there's no `gru projects add --url ...` remote-install flow.
- **Spec sharing across machines.** Each operator's `~/.gru/projects/` is local. Shared specs (team-wide environments) are a follow-up and would want content hashing, signing, or a registry.
- **Multi-tenancy on the project dir.** `~/.gru/projects/` is single-user. If two operators want to share a machine, they each have their own.
