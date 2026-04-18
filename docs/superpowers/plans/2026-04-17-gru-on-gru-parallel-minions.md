# Gru-on-Gru Parallel Minions — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans or subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Enable N parallel Gru minions against `~/workspace/gru`, dogfooding the v2 `command` adapter.

**Architecture:** Env-spec in `.gru/envs/minion-{fullstack,frontend}.yaml` using the `command` adapter. `LaunchSessionRequest` gains `env_spec_path`. `scripts/dev.sh` learns env-var overrides (`GRU_STATE_DIR`, `GRU_SERVER_PORT`, `GRU_WEB_PORT`, `GRU_SKIP_SERVER`). `gru server` learns `--port-file`. No port reservation — bind to `:0`, publish bound ports to `$GRU_STATE_DIR/urls.json`.

**Spec:** [`2026-04-17-gru-on-gru-parallel-minions-design.md`](../specs/2026-04-17-gru-on-gru-parallel-minions-design.md)

**Tech Stack:** Go 1.22+, connect-rpc, buf (proto), Cobra (CLI), bash, tmux.

---

## File Map

**Create:**
- `internal/env/spec/spec.go` — moved `loadSpecFile` + shared types (so both CLI and server can load spec YAMLs).
- `internal/env/spec/spec_test.go`
- `scripts/gru-env/minion/create.sh`
- `scripts/gru-env/minion/exec.sh`
- `scripts/gru-env/minion/exec-pty.sh`
- `scripts/gru-env/minion/destroy.sh`
- `scripts/gru-env/minion/status.sh`
- `scripts/gru-env/minion/events.sh`
- `.gru/envs/minion-fullstack.yaml`
- `.gru/envs/minion-frontend.yaml`
- `skills/gru/gru-on-gru-minion/SKILL.md`
- `docs/workflows/gru-on-gru-minions.md`

**Modify:**
- `cmd/gru/server.go` — add `--port-file` flag + listener-first startup so the bound port is known before write.
- `cmd/gru/cmd_env.go` — remove `loadSpecFile` (import from `internal/env/spec`).
- `cmd/gru/cmd_launch.go` — add `--env-spec` flag.
- `proto/gru/v1/gru.proto` — add `optional string env_spec_path = 7;`.
- `internal/server/service.go` — LaunchSession loads env spec, passes via controller opts.
- `internal/controller/controller.go` — `LaunchOptions.EnvSpec *env.EnvSpec`.
- `internal/controller/claude/controller.go` — swap `envAdp env.Environment` for `envRegistry *env.Registry` + `defaultAdapter string`; select adapter per launch.
- `scripts/dev.sh` — honor env vars; capture ephemeral ports.
- `CLAUDE.md` (if needed) — brief mention of the minion workflow.

---

## Tasks

### Task 1: `gru server --port-file` flag

- [ ] **Step 1:** Write failing test `internal/server/service_test.go` (new file or extend): given a `--port-file <tmp>` flag path, server writes `host:port` after bind.

Actually: the flag lives in `cmd/gru/server.go` (command wiring) and the write happens when `runServer` binds the listener. Cleanest test: unit-test the new `writePortFile(path, addr string) error` helper + integration test that spins up the server.

- [ ] **Step 2:** Implement: refactor `runServer()` to use an explicit `net.Listen` so we get the bound port from `ln.Addr()` before `httpServer.Serve(ln)`. Add flag wiring (`--port-file`). Write to `<path>.tmp` then `os.Rename`.

- [ ] **Step 3:** Run `go test ./cmd/gru/... -run PortFile`. Expect pass.

- [ ] **Step 4:** Manual verification: `go build -o /tmp/gru-dev ./cmd/gru && /tmp/gru-dev server --port-file /tmp/port &` → `sleep 1 && cat /tmp/port`. Expect `host:port`.

- [ ] **Step 5:** Commit.

### Task 2: `scripts/dev.sh` env-var overrides

- [ ] **Step 1:** Refactor `scripts/dev.sh` to read `GRU_STATE_DIR`, `GRU_SERVER_PORT`, `GRU_WEB_PORT`, `GRU_SKIP_SERVER`, `VITE_GRU_SERVER_URL` with defaults preserving current behavior.

- [ ] **Step 2:** When ports are `0`, pass `--port-file` to `gru server`; poll file until it exists (10s max); tail the web pipe for `Local:   http://localhost:PORT/` and capture port. Write `$GRU_STATE_DIR/urls.json`.

- [ ] **Step 3:** When `GRU_SKIP_SERVER=1`, skip the server startup entirely. `VITE_GRU_SERVER_URL` is NOT overwritten if pre-set.

- [ ] **Step 4:** Shell test: run with `GRU_STATE_DIR=/tmp/mtest GRU_SERVER_PORT=0 GRU_WEB_PORT=0 make dev` in background for 20s, then `cat /tmp/mtest/urls.json`. Verify both URLs non-empty. Kill it.

- [ ] **Step 5:** Commit.

### Task 3: Move `loadSpecFile` into `internal/env/spec`

- [ ] **Step 1:** Create `internal/env/spec/spec.go` exporting `LoadFile(path string) (env.EnvSpec, error)` (same logic as cmd/gru `loadSpecFile`).

- [ ] **Step 2:** Add `spec_test.go` with a table-driven test (valid file → ok; missing adapter → error; missing workdirs → error; `~` expansion? no, keep relative → absolute relative to file dir).

- [ ] **Step 3:** Rewire `cmd/gru/cmd_env.go` to call `spec.LoadFile`. Delete the duplicated helper.

- [ ] **Step 4:** `go test ./internal/env/spec/...`. Pass. `go test ./cmd/gru/...`. Pass.

- [ ] **Step 5:** Commit.

### Task 4: Proto — add `env_spec_path`

- [ ] **Step 1:** `proto/gru/v1/gru.proto` — add `optional string env_spec_path = 7;` to `LaunchSessionRequest`.

- [ ] **Step 2:** `make proto`. Regenerated Go + TS.

- [ ] **Step 3:** `go vet ./...` + `make lint`. Pass.

- [ ] **Step 4:** Commit generated + proto together.

### Task 5: `LaunchOptions.EnvSpec` + Controller refactor

- [ ] **Step 1:** Add `EnvSpec *env.EnvSpec` to `controller.LaunchOptions` (in `internal/controller/controller.go`).

- [ ] **Step 2:** Change `ClaudeController` signature: `NewClaudeController(apiKey, host, port string, envRegistry *env.Registry, defaultAdapter string) *ClaudeController`. Update `cmd/gru/server.go` caller.

- [ ] **Step 3:** Update `Launch`: if `opts.EnvSpec != nil`, resolve adapter from registry via `opts.EnvSpec.Adapter`; build the `env.EnvSpec` passed to `Create` from `*opts.EnvSpec` (set `Name = sessionID`, merge `Workdirs` = projectDir + addDirs). Otherwise build a host spec as today.

- [ ] **Step 4:** Update existing controller tests — pass a registry with just host adapter to preserve current behavior.

- [ ] **Step 5:** `go test ./internal/controller/... ./cmd/gru/... ./internal/server/...`. Pass.

- [ ] **Step 6:** Commit.

### Task 6: Server wire-up — LaunchSession loads env spec

- [ ] **Step 1:** In `internal/server/service.go::LaunchSession`: if `req.Msg.EnvSpecPath != nil && *req.Msg.EnvSpecPath != ""`, resolve path (relative → absolute against `projectDir`), call `spec.LoadFile`. On error, return `CodeInvalidArgument`. Pass loaded spec into `controller.LaunchOptions.EnvSpec`.

- [ ] **Step 2:** Add test `TestService_LaunchSession_WithEnvSpec` using a fake `command`-adapter YAML pointed at `test/fixtures/command-adapter/` scripts. Assert session is created and the controller received the spec.

- [ ] **Step 3:** `go test ./internal/server/...`. Pass.

- [ ] **Step 4:** Commit.

### Task 7: CLI — `--env-spec` flag

- [ ] **Step 1:** In `cmd/gru/cmd_launch.go` add `--env-spec <path>` flag; forward into `LaunchSessionRequest.EnvSpecPath`.

- [ ] **Step 2:** Manual: `gru launch --env-spec test/fixtures/command-adapter/spec.yaml --name test $PWD "echo hi"`. Expect success (after creating an adapter YAML for the fixture).

- [ ] **Step 3:** Commit.

### Task 8: `.gru/envs/minion-*.yaml`

- [ ] **Step 1:** Write `.gru/envs/minion-fullstack.yaml` and `.gru/envs/minion-frontend.yaml` per spec §3.

- [ ] **Step 2:** Commit.

### Task 9: `scripts/gru-env/minion/*.sh`

- [ ] **Step 1:** Write all six scripts, executable (`chmod +x`). Test standalone:
  - `SESSION=xyz scripts/gru-env/minion/create.sh xyz '{"mode":"fullstack"}'` — creates `~/.gru-minions/xyz/`, emits JSON.
  - `scripts/gru-env/minion/destroy.sh ~/.gru-minions/xyz` — removes it, exit 0.
  - Re-run destroy: still exit 0 (idempotent).

- [ ] **Step 2:** Run conformance: `gru env test .gru/envs/minion-fullstack.yaml`. Expect 8/9 (skip rehydrate-after-kill).

- [ ] **Step 3:** Commit scripts + any fixes.

### Task 10: `skills/gru/gru-on-gru-minion/SKILL.md`

- [ ] **Step 1:** Write the skill file per spec §5.

- [ ] **Step 2:** Commit.

### Task 11: `docs/workflows/gru-on-gru-minions.md`

- [ ] **Step 1:** Write operator-facing workflow doc: how to spawn, how to discover URLs, how to kill. Link from root README if sensible.

- [ ] **Step 2:** Commit.

### Task 12: End-to-end smoke test

- [ ] **Step 1:** With `make dev` (parent Gru) running in a monitor, invoke `gru launch --env-spec .gru/envs/minion-fullstack.yaml --name smoke-test --description "smoke" ~/workspace/gru "print your URLs from \$GRU_STATE_DIR/urls.json"`.

- [ ] **Step 2:** Verify `~/.gru-minions/<id>/` exists; `urls.json` appears after minion runs `make dev`; URLs resolve.

- [ ] **Step 3:** Kill session; verify teardown.

- [ ] **Step 4:** Document findings in the workflow doc; commit.

### Task 13: Code review pass

- [ ] Dispatch `superpowers:code-reviewer` agent with the spec + commit range; address feedback.

---

## Self-review

Spec coverage: each section of the spec maps to a task (piece 1→T2, piece 2→T1, piece 3→T8, piece 4→T9, piece 5→T10, piece 6→T4/5/6/7). Ephemeral port discovery → T2. Failure modes section → exercised in T12.
