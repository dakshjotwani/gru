# Minions Design v3 -- Final Review

**Spec reviewed:** `2026-04-11-minions-design-v3.md` (Draft v3 -- Go + gRPC + Claude Code intelligence)
**Reviewer:** Claude Opus 4.6
**Date:** 2026-04-10
**Prior reviews:** v1 review (all issues addressed), v2 discussion (all decisions incorporated)

---

## Executive Summary

v3 is a significant improvement over the original spec. The Go + gRPC + Claude Code sessions stack is coherent and well-motivated. The reviewer's critical issues from v1 (Resource Manager, Process Supervisor, adapter interface, auth, WebSocket protocol) have all been addressed. The discussion decisions (port management punt, skill-first agent awareness, session launch in Phase 1) are correctly reflected.

The spec is **nearly ready for implementation.** There are no architectural gaps remaining. What follows are consistency issues introduced by the v2-to-v3 rewrite, Go/gRPC-specific concerns not yet addressed, and ambiguities that would slow down a developer (or AI agent) picking up Phase 1.

---

## 1. Internal Consistency Issues

### [IMPORTANT] Phase labeling of SessionController contradicts Phase 1 scope

The adapter section (lines 114-115) states:

> **SessionController** -- stateful, manages launching, controlling, and querying sessions. Needed for Phase 2 (launching).

And the Go interface comment (line 153):

> `// --- Session control (Phase 2) ---`

But Phase 1 scope (line 571-572) now includes:

> - Quick launch: spawn an agent in a directory with a prompt from the dashboard
> - Kill switch: terminate a session via process signal

Quick-launch and kill both require `SessionController.Launch()` and `SessionHandle.Kill()`. The adapter section should say SessionController is needed in Phase 1 (for basic launch/kill), with the full `Capabilities()` set (pause, resume, inject context) arriving in Phase 2.

**Fix:** Change the adapter section to say SessionController is introduced in Phase 1 with `Launch` and `Kill` only. Phase 2 adds the remaining capabilities (pause, resume, inject context, full env lifecycle). Update the Go interface comments accordingly.

### [IMPORTANT] Resource Manager described but contradicts the "skip for MVP" decision

The discussion notes (Decision 4) explicitly decided: "No Resource Manager for ports. Setup scripts handle port allocation themselves." But the spec still describes a full Resource Manager component (lines 232-237) with port allocation, lease management, and zombie detection. The Session Launcher (line 241) still says "Acquires resources via Resource Manager (ports, session slot)."

Phase 2 scope (line 597) also lists "Resource Manager (port allocation, session slot enforcement)."

The decision was to skip port management complexity and let setup scripts handle it. The spec should be consistent with this.

**Fix:** Either:
- (a) Remove the Resource Manager component entirely and note that `max_concurrent_agents` is enforced by a simple counter in the session store, or
- (b) Keep the Resource Manager but explicitly scope it to session-slot counting only (no port allocation), and note that port management was deliberately deferred per the discussion decision. Remove "ports" from the acquire/release API and the "Single source of truth for port assignment" bullet.

Option (b) is recommended -- you still need something to enforce `max_concurrent_agents` and detect zombie sessions, but it should not claim to manage ports.

### [MINOR] "Port allocator" helper still in directory structure

The directory structure (line 309) lists:

> `helpers/    # Reusable scripts (worktree helper, port allocator)`

If port management is skipped, the "port allocator" reference is misleading. Change to just "worktree helper" or "reusable utility scripts."

### [MINOR] Config.yaml still defines `ports` resource

The config.yaml schema (line 361) still has:

```yaml
resources:
  ports: [8080-8099]
  max_concurrent_agents: 4
```

With port management punted, the `ports` field has no consumer in the system. Either remove it or add a comment that it is informational (consumed by setup scripts, not enforced by minions).

---

## 2. Go + gRPC Specific Concerns

### [IMPORTANT] grpc-web proxy strategy not specified

The spec correctly notes that browsers need grpc-web (lines 67, 103, 261), and the discussion notes mention "proxy or Envoy sidecar, or Buf's connect-web which speaks grpc-web natively." But the spec does not commit to an approach.

This matters for Phase 1 because the web dashboard is in scope. The developer needs to know: do I run a separate Envoy sidecar? Do I use `connect-go` to serve grpc-web directly from the Go process? Do I use `improbable-eng/grpc-web` Go middleware?

**Recommendation:** Use `connectrpc/connect-go` (formerly Buf Connect) which serves both native gRPC and grpc-web from the same Go HTTP handler. No sidecar needed. Single binary, single port. This is the simplest option for a single-user tool and avoids an Envoy dependency. Add this to the Tech Stack table.

The frontend reference to `@connectrpc/connect-web` (line 654) already implies this choice -- just make it explicit on the server side too.

### [IMPORTANT] No .proto file structure or key message definitions

The spec says ".proto files are the single source of truth for the API contract" (line 259) but provides no proto definitions or even a sketch of the service structure. For Phase 1, a developer needs to know at minimum:

- Service names and RPC signatures (`MinionsService.ListSessions`, `MinionsService.SubscribeEvents`, `MinionsService.LaunchSession`, `MinionsService.KillSession`)
- Key message types (`Session`, `Event`, `EventFilter`, `LaunchRequest`)
- Where the .proto files live in the repo

This does not need to be a complete proto file, but a sketch of the service definition would eliminate ambiguity.

**Recommendation:** Add a proto sketch section, something like:

```protobuf
service MinionsService {
  // Unary
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc GetSession(GetSessionRequest) returns (Session);
  rpc LaunchSession(LaunchSessionRequest) returns (LaunchSessionResponse);
  rpc KillSession(KillSessionRequest) returns (KillSessionResponse);

  // Server streaming
  rpc SubscribeEvents(EventFilter) returns (stream SessionEvent);
}
```

### [MINOR] Go module structure not specified

For a Go project, the module path and package structure matter for code generation (protoc-gen-go, sqlc) and for agents working on the codebase. A suggested layout:

```
cmd/minions/         # main binary (server + CLI)
internal/
  server/            # gRPC server implementation
  store/             # SQLite store (sqlc generated)
  adapter/claude/    # Claude Code adapter
  pipeline/          # Processing pipeline
  supervisor/        # Process supervisor
proto/               # .proto files
web/                 # React frontend
```

This is not critical for the spec but would help an AI agent scaffold the project correctly on day one.

### [MINOR] Event ingestion is HTTP but API is gRPC -- two listeners

The spec describes event ingestion as HTTP POST (for hooks) and the main API as gRPC. This means the Go server needs to listen on two protocols (or two ports). With `connect-go`, both can be served from the same `http.Handler` on a single port, since gRPC over HTTP/2 and regular HTTP/1.1 POST can coexist. The spec should note this explicitly to avoid confusion about whether there are one or two server ports.

---

## 3. Spec Readiness for Implementation

### [IMPORTANT] Hook script content not specified

Phase 1 depends on Claude Code hook scripts POSTing events to the backend. The spec says "thin hook scripts that POST events" but does not describe:

- Which Claude Code hooks to register (PreToolUse? PostToolUse? Notification? All of them?)
- The hook script template (bash script that curls the backend)
- How to install hooks (manual? `minions init` command?)
- What data is available in each hook event (this maps to Open Question 1, but Phase 1 cannot ship without answering it)

A developer cannot build the EventNormalizer for Claude Code without knowing the exact shape of hook event payloads.

**Recommendation:** Add a "Claude Code Hook Integration" subsection to Phase 1 scope or to the adapter section. It should list:
- Hooks to register: `PreToolUse`, `PostToolUse`, `Notification`, `Stop` (at minimum)
- Hook script template: `#!/bin/bash\ncurl -s -m 2 -X POST http://localhost:$MINIONS_PORT/events -H "Authorization: Bearer $MINIONS_API_KEY" -d "$CLAUDE_HOOK_DATA" &`
- Installation: `minions init` writes hook entries to `.claude/settings.json` (or user does it manually in MVP)

### [IMPORTANT] Session ID generation and correlation undefined

When a hook fires, how does the backend know which session it belongs to? The spec shows `session_id` in `MinionsEvent` but does not explain:

- Does Claude Code provide a session ID in hook events? If so, what is the field name?
- If not, how does minions correlate events to sessions? By PID? By working directory?
- For quick-launched sessions (Phase 1), minions spawns the process and knows the PID. But for externally started sessions (user runs `claude` manually), hooks fire but minions has no prior knowledge of the session.

This is critical for Phase 1 because the primary use case is monitoring sessions that are already running, not just sessions launched by minions.

**Recommendation:** Research Claude Code hook payloads to determine what session identifier is available. If Claude Code provides a session ID or conversation ID in hook data, document it. If not, define a correlation strategy (e.g., PID from hook environment, or first `session.start` event creates a session entry keyed by working directory + PID).

### [MINOR] "Intelligence sessions" management unclear

The processing pipeline spawns Claude Code (Haiku) sessions for classification (line 221). These are separate processes. The spec says they are "rate-limited (max 3 concurrent Haiku sessions)" but does not explain:

- Are these sessions visible in the dashboard? (The discussion notes suggest tagging them as "system sessions")
- Do they go through the same Process Supervisor?
- How are they spawned -- same `SessionController.Launch()` or a separate internal mechanism?

**Recommendation:** Add a brief note: intelligence sessions are internal, not shown in the dashboard by default, spawned via a dedicated internal launcher (not the user-facing `SessionController`), and managed by a simple goroutine pool with a concurrency limit.

---

## 4. Phase 1 Scope Check

Phase 1 now includes: monitoring + quick-launch + kill + CLI + web dashboard + desktop notifications + gRPC streaming + auth.

This is a substantial amount of work. Breaking it down:

| Component | Estimated effort |
|---|---|
| Go project scaffold + proto definitions + codegen | 0.5 days |
| SQLite schema + sqlc queries (sessions, events) | 0.5 days |
| HTTP event ingestion endpoint | 0.5 days |
| Claude Code EventNormalizer | 0.5 days |
| Hook script template + installation | 0.25 days |
| gRPC service implementation (list, get, subscribe) | 1 day |
| grpc-web setup (connect-go) | 0.25 days |
| Session quick-launch (spawn `claude` process, track PID) | 0.5 days |
| Kill switch (SIGTERM to process group) | 0.25 days |
| Process liveness polling (basic supervisor) | 0.5 days |
| Auth middleware (API key check) | 0.25 days |
| React dashboard (session grid, status cards, project groups) | 2 days |
| gRPC streaming client in React | 0.5 days |
| Desktop notifications (service worker) | 0.5 days |
| CLI commands (status, kill, launch, tail) | 1 day |
| Integration testing + polish | 1 day |

**Total: ~10 days.** This is tight for 2 weeks but feasible if AI agents are building it (per the discussion rationale for pulling launch into Phase 1). The risk is the frontend -- the dashboard is the most time-consuming piece and has the most ambiguity (no wireframes or component breakdown in the spec).

**Recommendation:** The scope is acceptable. To de-risk: build the backend + CLI first (days 1-5), then the dashboard (days 6-10). The CLI alone provides daily-driver value, so if the dashboard takes longer, Phase 1 is still useful. Consider adding a brief "Phase 1 implementation order" note to guide this.

---

## 5. Other Observations

### [IMPORTANT] No error handling or failure mode documentation

The spec describes happy paths well but does not address common failure modes:

- What happens when the Claude Code binary is not found or not installed?
- What happens when SQLite write fails (disk full, permissions)?
- What happens when a hook POST arrives for an unknown project?
- What happens when `minions launch` is called and the backend is not running?

These do not need exhaustive treatment, but a "Failure Modes" subsection would help implementers handle edge cases consistently.

### [MINOR] Config file location ambiguity

The spec references both `~/.minions/config.yaml` (line 272, for global config like API key) and `.minions/config.yaml` (line 240, for project config). These are different files in different locations. The naming overlap could cause confusion.

**Recommendation:** Rename the global config to `~/.minions/settings.yaml` or `~/.minions/server.yaml` to distinguish it from the per-project `.minions/config.yaml`.

### [MINOR] Attention decay formula may produce confusing UX

The attention scoring says scores decay by 0.9x per minute (line 428-429). A HIGH signal (10) decays to below the notification threshold (8) in about 2 minutes. This means if a user does not notice a notification within ~2 minutes, the session drops out of the attention queue even though the underlying problem (stuck, blocked) is still present.

**Recommendation:** Decay should only apply to transient signals (e.g., "just finished"). Persistent conditions (stuck, blocked) should not decay -- they should hold their score until the condition resolves (i.e., new events indicate progress). Add a `decays: bool` flag per signal type.

---

## Summary

| Category | Count |
|---|---|
| Consistency issues (IMPORTANT) | 2 |
| Consistency issues (MINOR) | 2 |
| Go/gRPC-specific (IMPORTANT) | 2 |
| Go/gRPC-specific (MINOR) | 2 |
| Implementation readiness (IMPORTANT) | 2 |
| Implementation readiness (MINOR) | 1 |
| Phase 1 scope | OK (tight but feasible) |
| Other (IMPORTANT) | 1 |
| Other (MINOR) | 2 |

**Verdict:** The spec needs targeted fixes (estimated 30-60 minutes of editing) before handing to an implementer. The two most impactful changes are:

1. Fix the Phase 1/Phase 2 labeling of SessionController to match the actual Phase 1 scope (quick-launch and kill are in Phase 1).
2. Resolve the Resource Manager inconsistency with the "skip port management" decision.

After those fixes plus the minor consistency cleanups, this spec is ready for Phase 1 implementation.
