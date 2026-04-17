---
name: gru:scaffold-env
description: Use when setting up a new Gru environment spec (from scratch) or auditing an existing project's env for Gru compatibility. Invoke whenever the user asks "how do I hook this project up to Gru" or "make this work with Gru."
---

# gru:scaffold-env — Get the operator's infrastructure talking to Gru

This skill helps the operator wire **any pre-existing local infrastructure** into Gru as an environment spec. Gru doesn't assume your project is a git repo checked into a sandbox — it attaches agents to **whatever you already have** (custom docker setups, embedded testers, bespoke scripts, multi-repo checkouts, non-code workdirs with lab tools).

The skill handles two on-ramps:

1. **From scratch** — the operator has no Gru env yet. Produce a minimum viable spec.
2. **Audit an existing env** — the operator has a working local setup (Dockerfile, `scripts/start-dev.sh`, Makefile, etc.) and wants Gru to attach to it. Produce a cost-report and an optional wrapper.

---

## When to use

Invoke this skill when any of the following are true:
- The operator says "I want to run Gru against this project" and there's no `env.yaml` / `environment_spec` anywhere in the repo.
- The operator has just installed Gru and is trying to launch their first session.
- The operator mentions custom infrastructure (lab rigs, embedded testers, docker-compose, bespoke scripts) and wants Gru to attach.
- The operator asks to validate a spec they wrote (`gru env test some-spec.yaml`).

**Do NOT** use this skill for:
- Questions about Claude Code itself (the agent runtime) — those belong in Claude Code's own docs.
- Questions about Gru's architecture, protocols, or internals — those are in `docs/superpowers/specs/2026-04-17-gru-v2-design.md`.
- Session launch, kill, attach — those are `gru launch`, `gru kill`, `gru attach` CLI commands; not a spec scaffolding concern.

---

## The two adapters

Gru ships two environment adapters. Pick one based on what the operator's existing setup looks like.

### `host` adapter
- Runs directly on the operator's machine.
- Config is empty. Workdirs are used as-is.
- **No isolation.** Two sessions see the same process tree, ports, and filesystem.
- Caps out at ~3–5 parallel sessions before port/cache collisions become painful.
- **Default choice** when the operator has no bootstrap script and "just" wants agents in their existing checkout.

### `command` adapter
- Wraps user-supplied shell scripts. The operator owns isolation, secrets, setup.
- Use when there's a custom bootstrap (docker-compose, JTAG pod, lab rig, data pipeline).
- Requires six scripts: `create`, `exec`, `exec_pty`, `destroy`, `events` (optional), `status` (optional).
- Scripts are text/templates rendered with `{{.SessionID}}`, `{{.Workdir}}`, `{{.Workdirs}}`, `{{.ProviderRef}}`, `{{.EnvSpecConfig}}`.
- Use this when the operator has an existing `scripts/start-dev.sh` or similar. Wrap it, don't replace it.

Decision rule: **host first unless the project visibly has custom infra**. Custom infra means any of: `Dockerfile`, `docker-compose.yml`, `scripts/*.sh`, `Makefile` with non-trivial targets, hardware/lab dependencies, multi-repo workspace.

---

## Flow 1 — Operator has no env yet (scaffold from scratch)

1. **Ask three targeted questions.** Do NOT over-interview.
   - *"What's the work? One sentence is fine."* — this shapes what `config.exec` looks like.
   - *"Does the project have a bootstrap script (Dockerfile, `scripts/start-dev.sh`, docker-compose, Makefile), or do you just run commands directly in the checkout?"*
   - *"Is this a single workdir or multiple (e.g. kernel + uboot + buildroot)?"*

2. **Propose the minimum adapter.**
   - No bootstrap + single workdir → `host` with a one-line `workdirs` list. Done.
   - Bootstrap script exists → `command` adapter wrapping it.
   - Multi-repo → list all workdirs; first is primary cwd, rest become `--add-dir`.

3. **Generate the spec file.** Write it to `./gru-env.yaml` (or wherever the operator asks). Never write into `.gru/`, `/etc/`, or `~/`.
   - For `host`: the YAML is 4 lines; write it inline.
   - For `command`: produce **up to 2 files** — the YAML spec + one `create.sh` (with inline comments for what destroy/exec would look like). If it needs more than 2 files to be correct, STOP and hand off to the operator with a note describing what else is needed. Don't generate half-correct scripts and hope the operator fills them in.

4. **Flag the secret foot-gun.** When generating `command` scripts, explicitly add a comment like:
   ```bash
   # SECURITY: Do NOT inline secrets (API keys, tokens, passwords) into
   # EnvSpec.Config. Anything in config: is persisted verbatim in Gru's
   # SQLite DB and rendered into script argv. Load secrets from the OS
   # keychain, 1Password CLI, sops, or env vars INSIDE this script.
   ```
   The operator's eyes will glaze over if you describe this in prose; inline is what sticks.

5. **Smoke-test via `gru env test`.** Run:
   ```bash
   gru env test ./gru-env.yaml
   ```
   - 9 conformance cases run: `Create`/`Destroy`/`Exec(echo hello)`/`ExecPty(stty size)`/`Rehydrate`/etc.
   - Pass → you're done. Tell the operator they can `gru launch` now.
   - Fail → report which case failed verbatim; don't second-guess.

---

## Flow 2 — Operator has existing env (audit + wrap)

1. **Read what's there.** Before any questions, actually read the files:
   - Every `Dockerfile`, `docker-compose.yml`, `*.nix`, `devcontainer.json` in the repo.
   - `scripts/*.sh`, `Makefile`, `README.md`, `CONTRIBUTING.md`, `docs/setup*` — anywhere a bootstrap sequence lives.
   - `.envrc` (direnv), `.tool-versions` (asdf) — hints about managed-runtime tooling.

2. **Audit against the contract.** Produce a short cost-report with three sections. Do NOT give a verdict like "ready" or "not ready" — the operator decides what's good enough.

   ```
   # Gru compatibility cost-report for <project>

   ## What's there today
   - bootstrap: scripts/start-dev.sh (runs docker-compose up + waits for postgres)
   - tmux: available (required for persistent pty)
   - process persistence: docker containers survive host reboots — OK

   ## Known failure modes at N sessions
   - port 3000 collision: the web server binds :3000 on host; parallel sessions
     would fight over it. Fix: bind to ephemeral port, or use `command` adapter
     with per-session docker networks.
   - shared postgres volume: two sessions writing the same DB corrupt state.
     Fix: `command` adapter with per-session db namespace.

   ## Cheapest wrapper
   - `command` adapter wrapping `scripts/start-dev.sh`
   - `create.sh`: allocate a unique compose project name (<session_id>), write to .env, docker-compose up
   - `destroy.sh`: docker-compose -p <session_id> down -v
   - Estimated effort: 30 minutes
   ```

   Keep it to 30 lines or so. The operator reads this, not a 500-word essay.

3. **Offer the wrapper.** If the cost-report shows a `command`-adapter wrapper is viable, offer to generate it. If the operator says yes, follow Flow 1 step 3–5 (generate, flag secrets, smoke-test).

4. **Don't scope-creep.** The `scaffold-env` skill produces at most 2 generated files. If the env needs more (docker tweaks, new scripts, CI changes), list them as follow-ups for the operator, do not silently generate them.

---

## The spec file format

```yaml
name: my-project
adapter: host | command
workdirs:
  - /absolute/path/to/primary-cwd
  - /absolute/path/to/secondary-workdir  # optional, becomes --add-dir

# Only for adapter: command
config:
  create:   "scripts/gru-env/create.sh {{.SessionID}} {{.Workdir}}"
  exec:     "scripts/gru-env/exec.sh {{.ProviderRef}}"
  exec_pty: "scripts/gru-env/exec-pty.sh {{.ProviderRef}}"
  destroy:  "scripts/gru-env/destroy.sh {{.ProviderRef}}"
  events:   "scripts/gru-env/events.sh {{.ProviderRef}}"   # optional
  status:   "scripts/gru-env/status.sh {{.ProviderRef}}"   # optional
```

Run `gru env show host` or `gru env show command` from the CLI to print this schema with inline commentary.

---

## Hard constraints

- **Never write outside the repo.** Don't put anything in `~/.gru/`, `/etc/`, `/usr/local/`, or the operator's home dir. The spec lives in the project; the operator controls where.
- **Never inline secrets.** Even in example configs. Always show the keychain/1Password/env-var pattern.
- **Generated scripts get a disclaimer at the top.**
  ```bash
  # Generated by `gru:scaffold-env` on <date>. Review before running.
  # You own this file — edit freely.
  ```
- **2-file cap on generation.** If the env requires more, produce 2 files and a markdown follow-up list.

---

## Reference: the six `command` scripts

If you need to show the operator what a full set looks like, point them at the reference scripts that ship with Gru:

```
test/fixtures/command-adapter/
├── create.sh
├── destroy.sh
├── events.sh
├── exec.sh
├── exec-pty.sh
└── status.sh
```

These are the canonical examples — they implement a trivial "sandbox per session is a tmp dir" env and pass the full 9-case conformance suite. Operators can copy them as starting points.
