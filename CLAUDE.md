# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Gru

Gru is a mission control system for managing fleets of Claude Code agent sessions. It monitors, launches, and manages sessions across projects via a Go gRPC backend, SQLite persistence, and a React web dashboard.

## Build & Development Commands

`make` self-bounces into the Nix dev shell (`nix develop`) when `IN_NIX_SHELL` is unset, so callers don't need to enter the shell manually. For ad-hoc shell access: `nix develop -c bash`.

```bash
make build          # Build the Go CLI binary
make test           # Run all Go tests (go test ./...)
make lint           # Run buf lint + go vet
make proto          # Generate protobuf code (Go + TypeScript)
make sqlc           # Generate typed SQL queries
make generate       # Run both proto and sqlc
make dev            # Start dev server + web dashboard (scripts/dev.sh)
make doctor         # Check that required tools are on PATH
```

Frontend (from `web/` directory):
```bash
npm install         # Install frontend dependencies
npm run dev         # Start Vite dev server (port 3000)
npm test            # Run Vitest tests
```

Run a single Go test:
```bash
go test ./internal/adapter/... -run TestNormalizerName
```

## Architecture

State pipeline rev 2 (see `docs/superpowers/specs/2026-04-24-state-pipeline-design.md`):
the producer side is a **transcript tailer** rather than an HTTP hook receiver.

```
Claude Code Processes (tmux windows)
  ├─ writes ~/.claude/projects/<hash>/<sid>.jsonl   ← producer; Gru never makes
  └─ Notification hook → ~/.gru/notify/<sid>.jsonl     a network call
  ▼
Gru Server (port 7777)
  ├── per-session Tailer goroutine — reads JSONL, applies state derivation,
  │     writes events projection + sessions row in one SQLite transaction
  ├── Publisher — tails events.seq, fans out to subscribers
  │     (close-on-overflow, NOT silent-drop)
  ├── Supervisor — tmux liveness probe; emits claude_pid_exit synthetic
  │     events into ~/.gru/supervisor/<sid>.jsonl (the tailer reads it)
  └── gRPC service (connect-rpc over h2c)
        ListSessions / LaunchSession / KillSession /
        SubscribeEvents (with since_seq for replay-on-reconnect)
  ▼
React Dashboard (port 3000) — renders dumb; trusts session.status from the server
```

### Key data flow

1. **Launch**: CLI/web calls `LaunchSession` → server creates DB record → `ClaudeController` spawns tmux window. The tailer manager spawns a per-session goroutine immediately.
2. **State derivation**: each Tailer reads new bytes from its transcript JSONL, applies the pure `state.Derive` function, and writes both the events projection row and the derived sessions row in one SQLite transaction.
3. **Notifications**: only `Notification` hooks fire; the script appends to `~/.gru/notify/<sid>.jsonl`. The tailer reads that file as a second input.
4. **Monitoring**: Web UI calls `SubscribeEvents(since_seq=N)` → server replays every event with seq > N, then streams live events.
5. **Supervision**: Supervisor reconciles tmux panes; when one disappears, it appends a `claude_pid_exit` synthetic line to `~/.gru/supervisor/<sid>.jsonl`. The tailer (single status writer) turns that into errored/completed via the derivation function.

### Component layout

- `cmd/gru/` — Cobra CLI (server start, session launch/kill/attach/tail/prune, `env list/show/test`)
- `proto/gru/v1/gru.proto` — Service and message definitions (source of truth for API)
- `internal/server/` — gRPC service handlers, auth interceptor, CORS
- `internal/controller/` — Pluggable session launchers (ClaudeController uses tmux)
- `internal/adapter/` — Legacy event normalizers; rev-2 keeps the registry but the HTTP path is gone
- `internal/env/` — Environment abstraction: `host` and `command` adapters, `PersistentPty` layer, 9-case conformance suite. See `docs/superpowers/specs/2026-04-17-gru-v2-design.md`.
- `internal/state/` — Pure derivation function (`state.Derive`) — single source of truth for status transitions
- `internal/tailer/` — Per-session goroutine that reads Claude's JSONL + the notify file + the supervisor file, runs derivation, commits to SQLite
- `internal/publisher/` — Tails events.seq, fans out to subscribers, closes on overflow
- `internal/store/` — SQLite with WAL mode, sqlc-generated queries, migrations
- `internal/supervisor/` — tmux liveness probe; emits `claude_pid_exit` events into `~/.gru/supervisor/<sid>.jsonl`
- `internal/attention/` — Hook-driven attention score engine (paused / notification / tool_error / staleness weights, tunable via `~/.gru/server.yaml`)
- `skills/` — Claude Code skills shipped in the repo (symlink into `.claude/skills/` for discovery)
- `test/fixtures/command-adapter/` — reference create/exec/destroy scripts; double as `command` adapter conformance targets
- `web/src/` — React 19 + Vite + connect-web client

## Code Generation

Protobuf and SQL queries are generated — do not edit generated files directly:
- `proto/gru/v1/*.proto` → `go tool buf generate` → Go code in `internal/` + TypeScript in `web/src/gen/`
- `internal/store/queries/*.sql` → `go tool sqlc generate` → `internal/store/db/`

Both buf and sqlc are managed as Go tools (declared in `go.mod`), no separate installation needed.

## Design Patterns

- **Registry pattern**: Adapters and controllers are registered by runtime name, making it extensible beyond Claude Code. The `env.Registry` does the same for environment adapters (`host`, `command`).
- **Pub/Sub streaming**: In-memory Publisher broadcasts events to all active `SubscribeEvents` subscribers
- **Session status workflow**: `starting` → `running` → `idle`/`errored`/`killed`; `attention_score` is written by the attention engine on every hook event, `0` on terminal states
- **Session lookup files**: `.gru/sessions/<short_id>` files let hook scripts resolve session IDs without env var reliance
- **Environment contract**: Any Environment adapter implements Create/Rehydrate/Exec/ExecPty/Destroy/Events/Status. `PersistentPty` sits on top of an Environment, keeping tmux sessions alive across Gru restarts. See the v2 design spec for the full contract and `internal/env/conformance` for the 9-case test harness

## Configuration

- Server config: `~/.gru/server.yaml` (generated by `scripts/dev.sh` with stable API key)
- Logs (when running via `make dev` / direct invocation): `~/.gru/logs/`
- Logs (when supervised by `scripts/install-gru.sh`):
  - macOS (launchd): `~/Library/Logs/gru/server.log`
  - Linux (systemd user unit): `journalctl --user -u gru-server` (stdout/stderr → journal)
- Hook script: `hooks/claude-notify.sh` (the only hook in rev 2; symlinked from `~/.gru/hooks/`)
- Claude Code hooks: `.claude/settings.json` registers `claude-notify.sh` for the `Notification` event only — everything else flows through the transcript tailer
- Frontend env vars: `VITE_GRU_SERVER_URL`, `VITE_GRU_API_KEY`

## Agent skills

### Issue tracker

Issues are tracked in GitHub Issues at `dakshjotwani/gru` via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default label vocabulary (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `CONTEXT.md` + `docs/adr/` at the repo root. See `docs/agents/domain.md`.
