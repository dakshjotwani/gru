# Minions Design Specification -- Review

**Spec reviewed:** `2026-04-11-minions-design.md` (Draft)
**Reviewer:** Claude Opus 4.6
**Date:** 2026-04-10

---

## Executive Summary

This is a well-structured spec with a clear vision, crisp responsibility boundaries, and a sensible phasing plan. The core architecture -- event bus with processing pipeline, runtime adapters, and a SQLite-backed single-user backend -- is the right shape for the problem. However, the spec has meaningful gaps in concurrency/resource management, the runtime adapter contract is underspecified for real implementation, and several Phase 1 scoping decisions will create friction that could be avoided with modest additions. The issues below are fixable without rethinking the architecture.

---

## Issues by Category

### 1. Architectural Gaps

#### [CRITICAL] No concurrency or resource arbitration model

The spec mentions `resources.max_concurrent_agents: 4` and port pools in config.yaml, but never specifies the component that enforces these. When the Session Launcher spawns session 5 for a project with a limit of 4, what happens? When two sessions both need port 8080, who allocates?

There is no resource manager described in the component breakdown. The Session Launcher "loads project config" and "runs environment setup scripts," but resource allocation is a distinct concern that must happen *before* environment setup and must be *released* atomically on teardown.

**Proposed fix:** Add a **Resource Manager** component to the architecture. It should:
- Maintain a resource allocation table in SQLite (ports, worktree slots, session slots per project)
- Provide `acquire(projectId, resources) -> Lease` and `release(leaseId)` operations
- Be the single source of truth for port assignment, enforcing the `ports` range from config
- Reject launch requests when `max_concurrent_agents` is reached (or queue them)
- Handle lease expiry for zombie sessions (session crashes without teardown)

DAKSH: maintaining valid ports in a db is kinda weird. what if an app external to minions takes one of these ports? i think it's ok to do this for resource conflicts between minions managed stuff but minions cannot necessarily guarantee the port isn't taken by something else. Lmk your thoughts but I feel like for something running locally we can say fuck it (assume configs handle this well and agents can bring up issues if something goes wrong) and not worry about the complexity this introduces. Do we need to architect this now or would this be easy to architect and introduce later after an mvp?

DAKSH: Unrelated to this section, but I think we're going to need to make it clear to every agent that it is being managed by minions: provide them with the context on what that means, when they should stop and bubble info, when they should bubble up info, etc. Also would we need an MCP for agents to get info from minions or to communicate custom events? Please research the claude code sdk thoroughly before answering that.

#### [CRITICAL] No process supervision or crash recovery strategy

The Session Launcher spawns agent processes, but the spec does not describe what happens when:
- An agent process crashes (SIGKILL, OOM, machine restart)
- The minions backend itself crashes and restarts
- Environment setup succeeds but agent spawn fails (partial setup state)
- Teardown scripts fail or hang

Open Question 4 touches on backend restart, but the problem is broader. There is no described heartbeat, process monitoring loop, or orphan detection mechanism.

**Proposed fix:** Add a **Process Supervisor** subsection to the Session Launcher component:
- Poll running sessions periodically (check PID liveness)
- On detecting a dead process: run teardown, release resources, update session status, emit event
- On backend startup: scan for sessions marked "running" in the database, verify PID liveness, reconcile
- Teardown scripts must have a configurable timeout with a hard-kill fallback
- Environment setup must be transactional: if agent spawn fails after setup, rollback runs automatically (this is implied but should be explicit)

#### [IMPORTANT] WebSocket fan-out and connection lifecycle unspecified

The spec says frontends consume a WebSocket but does not specify:
- Is there one WebSocket per frontend connection, or a shared event bus?
- What happens when the dashboard disconnects and reconnects? Does it get a snapshot of current state, or just future events?
- What is the subscription model? All events? Per-project? Per-session?

This matters for Phase 1 because the dashboard must render correct state on first load and after network interruptions.

**Proposed fix:** Add a WebSocket protocol section:
- On connect: server sends a full state snapshot (all active sessions with current status)
- After connect: server pushes incremental events
- Support subscription filters: `{ projects: ["av-sim"], minAttention: 5 }` (optional, can be all-sessions in MVP)
- Client reconnect: re-sends snapshot, client reconciles

#### [IMPORTANT] No authentication or authorization model

The spec describes a backend that accepts HTTP POSTs from hooks and exposes a REST/WebSocket API. There is no mention of authentication. For a single-user local setup this might be acceptable in MVP, but:
- Open Question 3 explicitly envisions multi-machine operation over a Tailnet
- The kill switch (Phase 1) terminates processes -- unauthenticated kill endpoints are dangerous
- Hook endpoints that accept arbitrary JSON without auth are an injection surface

**Proposed fix:** Add an auth section:
- MVP: pre-shared API key in config, sent as `Authorization: Bearer <key>` header. Hooks include it via environment variable.
- Later: Tailscale auth integration (which is free if already on a Tailnet)
- Document the threat model explicitly: "single user, trusted network in MVP; auth required for multi-machine"

#### [MINOR] No explicit data retention or pruning strategy

SQLite is the right choice for MVP, but with 10-20 concurrent sessions generating continuous events, the database will grow quickly. The spec does not describe event retention, archival, or pruning.

**Proposed fix:** Add a retention policy section:
- Events older than N days (configurable, default 30) are pruned or archived
- Session metadata and insights are retained longer (90 days default)
- Knowledge entries are permanent
- Add a `minions db prune` CLI command

DAKSH: Can you present to me a couple DB/event queue options here? for session metadata/insights and knowledge yeah i can see SQLite or postgres making sense but for the events I wanna understand if there are better solutions.

---

### 2. Spec Completeness

#### [CRITICAL] Runtime Adapter interface is too vague to implement against

The adapter interface table lists four concerns (Event Source, Session Launcher, Session Control, Session Metadata) with one-liner descriptions. This is insufficient for someone implementing a second adapter. Key questions unanswered:

- **Event Source:** What is the minimum set of events an adapter must emit? The spec says events are "normalized" but never provides a normalization contract -- just one example schema. What are the required event types? What payload fields are mandatory per type?
- **Session Launcher:** What is the function signature? What does the adapter return -- a PID? A handle? A session ID? How does the caller know when the session has successfully started (vs. crashed on launch)?
- **Session Control:** "Process signals, stdin, file-based" is an implementation note, not a contract. What operations must every adapter support? Is `kill` mandatory? Is `pause` optional? Is `injectContext` best-effort?
- **Session Metadata:** What is the minimum metadata contract? Session ID and working dir seem mandatory; subagent tree is Claude Code-specific.

**Proposed fix:** Define the adapter as a TypeScript interface:

```typescript
interface RuntimeAdapter {
  readonly runtimeId: string;  // "claude-code" | "codex" | ...

  // Launch a new session. Returns a handle for control operations.
  launch(options: LaunchOptions): Promise<SessionHandle>;

  // Normalize a raw event from this runtime into the common schema.
  normalizeEvent(rawEvent: unknown): MinionsEvent;

  // Required capabilities (adapter declares what it supports)
  capabilities: Set<'kill' | 'pause' | 'resume' | 'injectContext'>;
}

interface SessionHandle {
  sessionId: string;
  pid: number;
  kill(): Promise<void>;
  injectContext?(context: string): Promise<void>;
  onExit(callback: (code: number) => void): void;
}
```

Also define the required event types: `session.start`, `session.end`, `tool.pre`, `tool.post`, `notification`, `error`. Everything else is optional.

#### [IMPORTANT] Environment setup/teardown lifecycle has edge cases not addressed

The config.yaml shows `setup`, `teardown`, and `rollback` scripts, but the spec does not define:
- What happens if a setup script fails midway through the list? Does it rollback only the steps that succeeded, or run the full rollback script?
- What does "idempotent rollback" mean precisely? Can rollback be run even if setup never ran? (It should be, for crash recovery.)
- How does the `healthcheck` URL work? Polling? Timeout? Retry count?
- What is the `{{PORT}}` template syntax? Is it only `PORT`, or arbitrary variables? Where do they come from?

**Proposed fix:** Add an Environment Lifecycle section specifying:
- Setup scripts run in order; on failure at step N, rollback runs (not reverse-order teardown of steps 1..N -- rollback must be written to be idempotent regardless of partial state)
- Healthcheck: poll with configurable interval (default 2s), timeout (default 60s), and retry count (default 30)
- Template variables: `{{PORT}}` is allocated by the Resource Manager; define additional supported variables (`{{PROJECT_DIR}}`, `{{SESSION_ID}}`, `{{WORKTREE_DIR}}`)
- Teardown runs on normal session exit; rollback runs on crash/failure (teardown and rollback may be the same script, but the distinction should be explicit)

#### [IMPORTANT] Attention scoring weights are described but the algorithm is not

The spec lists signal weights (HIGH/MEDIUM/LOW) but does not describe how they combine into a numerical score. Is it a weighted sum? Max of active signals? Does the score decay over time (a session that was stuck 30 minutes ago but is now making progress)?

**Proposed fix:** Define the scoring algorithm:
- Each signal has a weight: HIGH=10, MEDIUM=5, LOW=1 (tunable in config)
- Score = max(active_signal_weights), not sum (a session can only need so much attention)
- Scores decay: multiply by 0.9 every minute since the triggering event, with a floor of 0
- New events reset the relevant signal's timestamp
- Threshold for notification: configurable, default 8 (triggers on any HIGH signal)

#### [MINOR] "Chat with Minions" has no described implementation approach

The chat panel is listed as Phase 3 scope but has no implementation details. Is it a Claude API call with the fleet state as context? Does it have tool use to query the database? Can it execute actions (spawn, kill)?

**Proposed fix:** Add an implementation sketch:
- Uses Claude API with a system prompt that includes current fleet state summary
- Has structured tool definitions: `querySession`, `queryCost`, `spawnAgent`, `killSession`
- Actions that mutate state (spawn, kill) require a confirmation step in the UI
- Conversation history is session-scoped (each dashboard session has its own chat context)

DAKSH: Do we need claude API? or can we just run another claude code session internally and provide it with its role and this context. thoughts?

---

### 3. Phase Dependencies

#### [IMPORTANT] Phase 2 depends on concepts not fully available until Phase 3

Phase 2 (Launch) includes "context injection" in scope, but context injection as described in the Interfaces section relies on understanding *when* context injection is useful -- which is an intelligence layer concern (Phase 3). In Phase 2, context injection would be a raw "push text to agent" button with no intelligence about timing or relevance.

This is probably fine in practice -- a raw text injection button is still useful -- but the spec should explicitly acknowledge this is a basic version that gets smarter in Phase 3.

**Proposed fix:** In Phase 2 scope, clarify: "Context injection (basic: manual text push to running agent; Phase 3 adds intelligent timing and suggestions)"

#### [IMPORTANT] Phase 5 dependency list appears to have a typo

Phase 5 states: "Depends on: Phase 3 (needs launcher), Phase 2 (needs intelligence for matching)." This looks reversed. Phase 2 is the launcher; Phase 3 is intelligence. The dependency should read: "Depends on: Phase 2 (needs launcher), Phase 3 (needs intelligence for matching)."

**Proposed fix:** Swap the dependency descriptions.

#### [MINOR] Phase 1 "kill switch" requires runtime adapter knowledge not yet formalized

Phase 1 includes "Kill switch: terminate a session from the dashboard." But Phase 1 has no runtime adapter interface (that is Phase 2). In Phase 1, killing would mean sending SIGTERM to a PID discovered from hook events. This works for Claude Code specifically, but the spec should acknowledge this is a runtime-specific shortcut that gets generalized in Phase 2.

**Proposed fix:** In Phase 1 scope, note: "Kill switch: terminate a session via process signal (Claude Code-specific; generalized to runtime adapter in Phase 2)"

---

### 4. Technical Feasibility

#### [IMPORTANT] Bun + SQLite concurrency under event load

With 10-20 concurrent sessions, each emitting events on every tool call, the backend will see bursts of 50-100 events/second during active periods. Bun's SQLite binding (`bun:sqlite`) is synchronous by default (it uses the SQLite WAL mode but calls are blocking on the JS thread). Since the processing pipeline runs async Claude API calls, the event ingestion path itself should be fine, but:

- Write contention: if the processing pipeline writes insights back to SQLite concurrently with event ingestion, WAL mode handles this but there can be `SQLITE_BUSY` errors under load.
- The processing pipeline triggers Claude API calls for classification -- if these are slow (>1s), and 20 sessions each need classification, there could be a queue of 20+ pending API calls. The spec does not describe rate limiting or batching for Claude API calls.

**Proposed fix:** Add to the tech stack section:
- SQLite: use WAL mode (already default in Bun), add retry-on-busy logic with exponential backoff
- Claude API calls: implement a rate-limited queue (e.g., max 5 concurrent classification calls); batch classification where possible (classify multiple sessions in one API call if they are new)
- Consider using Bun's worker threads for the processing pipeline to avoid blocking the event ingestion path

#### [IMPORTANT] Process management via `child_process` is fragile for long-lived sessions

The tech stack recommends `child_process` / Bun shell for launching `claude` processes. For sessions that run for hours:
- The minions backend process is the parent -- if it restarts, all child processes become orphans (re-parented to init, but minions loses the PID mapping)
- Stdout/stderr buffering of child processes can cause memory pressure if not properly drained
- No mention of process groups -- killing a session should kill the entire process tree, not just the top-level PID

**Proposed fix:** Add process management details:
- Spawn agents in their own process group (`detached: true` + `process.setpgid`)
- Store PID (and PGID) in the database so they survive backend restarts
- Kill sends `SIGTERM` to the process group, with a `SIGKILL` fallback after timeout
- Consider using a lightweight process manager (e.g., spawn via `setsid`) so sessions survive backend restarts gracefully
- Alternatively, document that backend restart = session restart (simpler, acceptable for MVP)

#### [MINOR] Hook scripts as `curl` POST have failure modes

The hook scripts are described as thin `curl POST` commands. If the minions backend is down or slow:
- `curl` will block the Claude Code hook execution. If Claude Code hooks are synchronous (blocking the agent), this adds latency to every tool call.
- Lost events mean incomplete session tracking. There is no retry or local buffering.

**Proposed fix:** Add hook resilience:
- Hooks should use `curl` with a short timeout (e.g., 2 seconds) and `--fail-silently` (or background the POST with `&` if the hook mechanism allows it)
- Optionally: hooks append to a local file; a sidecar process tails the file and POSTs to the backend (decouples hook execution from network)
- Document whether Claude Code hooks are blocking or async -- this determines the approach

---

### 5. Runtime Adapter Abstraction

#### [IMPORTANT] Adapter abstraction conflates event ingestion with session control

The current adapter interface has four concerns lumped together. In practice, the Event Source concern flows in the opposite direction from the other three:
- **Event Source:** runtime pushes to minions (inbound)
- **Session Launcher / Control / Metadata:** minions calls into the runtime (outbound)

For Claude Code, the event source is hook scripts (HTTP POST) that run outside the adapter code entirely. The adapter itself only handles the outbound direction. This means the "adapter" is really two separate things: an event normalizer (stateless, runs in the ingestion pipeline) and a session controller (stateful, manages processes).

**Proposed fix:** Split the adapter into two interfaces:
1. **EventNormalizer** -- stateless, registered per runtime type. Called during event ingestion to normalize raw events.
2. **SessionController** -- manages launching, controlling, and querying sessions for a specific runtime. This is the stateful part.

This separation makes it clearer that a new runtime only needs to implement `EventNormalizer` for Phase 1 (monitoring), and `SessionController` for Phase 2 (launching).

#### [MINOR] No adapter versioning or capability negotiation

Different runtimes (and different versions of the same runtime) will support different hook events and control mechanisms. The spec has no versioning for the adapter interface or capability flags beyond the implicit "what methods exist."

**Proposed fix:** Add `capabilities` to the adapter interface (see the TypeScript interface proposed above). The frontend can then show/hide controls based on what the adapter supports (e.g., hide "inject context" if the adapter does not support it).

---

### 6. MVP Scope Assessment

#### [IMPORTANT] MVP lacks session launching -- this limits the "daily driver" value

The spec explicitly excludes session launching from Phase 1: "you still launch `claude` manually; minions just watches." For a user running 10-20 concurrent sessions, the monitoring dashboard alone is useful, but the *pain* of managing sessions is in launching them with the right environment. Phase 1 becomes a "nice to have" visibility tool, not a daily driver that changes workflow.

**Proposed fix:** Consider pulling the simplest possible launch capability into Phase 1:
- A "quick launch" button that runs `claude --resume` or `claude -p "prompt"` in a specified directory
- No environment setup, no agent profiles, no health checks -- just "spawn claude in this directory with this prompt"
- This turns minions from "a dashboard you check" into "the place you start and watch sessions" from day one

Alternatively, if you want to keep Phase 1 scope tight, add a Phase 1.5 milestone: basic launch (no env setup) that can be built in a day after Phase 1.

DAKSH: I think this is important. I do think session management is a key part of this product and we should pull it into phase 1. I agree we can keep it simple but AI agents are going to build this so there may not be a human implementation bottleneck to justify splitting this into two phases. The value I see in phases is that it provides a working solution before moving on to improvements, and I think at least session launching and viewing is super useful for phase 1. Also will I be able to manually enter/attach to a session to see what's going on and chime in? I'm gonna want this for sure.

#### [IMPORTANT] No CLI interface for MVP

The spec describes a web dashboard as the MVP frontend. For a power user running 10-20 sessions, a CLI is often faster than switching to a browser. Common operations:
- `minions status` -- fleet overview
- `minions launch av-sim feature-dev "implement the parking feature"`
- `minions kill session-123`
- `minions attention` -- what needs me?

**Proposed fix:** Add a minimal CLI to Phase 1 or Phase 1.5:
- `minions status` (list sessions, one line each)
- `minions kill <session-id>`
- `minions tail <session-id>` (stream events from a session)
- The CLI just calls the REST API -- minimal implementation cost

#### [MINOR] Desktop notifications via browser Notification API require the dashboard to be open

The spec uses browser Notification API for desktop notifications. This only works if the dashboard tab is open. For a daily driver, notifications should work even when the browser tab is closed.

**Proposed fix:** Options:
- Use a service worker for background notifications (works in Chrome even when tab is closed, but not all browsers) DAKSH: do this please if possible
- Add native notification support via `node-notifier` or Bun equivalent as a backend-side option
- MVP compromise: document the limitation; the CLI polling approach (`minions watch --notify`) is a backup

---

### 7. Open Questions Assessment

The five open questions are relevant, but there are several obvious gaps:

#### Missing Open Questions

**[IMPORTANT] How does minions handle worktree management?**

For 10-20 concurrent sessions on the same repo, each session likely needs its own git worktree. The spec mentions worktree helpers in `.minions/helpers/` but never describes the worktree lifecycle: who creates worktrees? Who cleans them up? Does the Session Launcher create one per session? Is there a pool? This is one of the most operationally important questions for the target use case.

**Proposed addition:** Add as Open Question 6: "Worktree management: Does minions manage git worktrees for concurrent sessions on the same repo? If so, how are they allocated, named, and cleaned up? If not, how does the user manage this manually at scale?"

Suggested answer: Minions should own worktree lifecycle for launched sessions. The Session Launcher creates a worktree (named `minions-<session-id>`) from the configured base branch, the agent works in it, and teardown removes it (or marks it for manual cleanup if the session produced commits).

**[IMPORTANT] What is the cost model for the intelligence layer?**

The intelligence layer makes Claude API calls for classification, stuck detection, and summaries. With 10-20 sessions, each getting classified and periodically re-evaluated, plus periodic summaries, the API cost of running minions itself could be significant. The spec does not estimate costs or discuss strategies to minimize them.

**Proposed addition:** Add as Open Question 7: "What is the expected Claude API cost of running the intelligence layer for 10-20 concurrent sessions? What are the strategies to keep this manageable?"

Suggested answer: Use Haiku for all classification and stuck detection (stated but worth quantifying). Use prompt caching aggressively. Batch classifications. Budget estimate: ~$2-5/day for intelligence layer at 20 sessions (mostly Haiku calls).

**[IMPORTANT] How does minions discover existing projects?**

The spec describes project config in `.minions/config.yaml`, but never explains how minions knows which projects exist. Is there a registry? Does the user add them manually? Does minions scan a directory tree?

**Proposed addition:** Add as Open Question 8: "Project discovery: how does minions know about projects? Manual registration, directory scan, or config file?"

Suggested answer: MVP uses a global `~/.minions/projects.yaml` listing project paths. Phase 2 could add `minions init` to register a project and auto-discovery from git repos in configured directories.

**[MINOR] How does the "use minions to build minions" bootstrap actually work?**

The spec mentions this as a goal but never describes the mechanics. Once Phase 1 is running, what does "use minions to build Phase 2" look like concretely? This should be sketched out to validate the phasing.

**Proposed addition:** Add a short "Bootstrap Plan" subsection:
- Build Phase 1 manually (one Claude Code session)
- Deploy Phase 1 locally, start using it
- For Phase 2: launch 2-3 Claude Code sessions manually; monitor them in the Phase 1 dashboard
- For Phase 3+: use Phase 2's launcher to spawn sessions for Phase 3 development

---

## Suggested Additions (Summary)

| Addition | Where | Priority |
|---|---|---|
| Resource Manager component | Architecture > Component Breakdown | Critical |
| Process Supervisor subsection | Session Launcher component | Critical |
| Full RuntimeAdapter TypeScript interface | Agent Runtime Adapter section | Critical |
| WebSocket protocol spec (snapshot + incremental) | Interfaces section | Important |
| Auth model (even if just API key for MVP) | New section after Architecture | Important |
| Environment lifecycle edge case handling | Project Configuration | Important |
| Attention scoring algorithm definition | Intelligence Layer | Important |
| Claude API rate limiting and batching | Tech Stack | Important |
| Process group management details | Tech Stack | Important |
| Worktree management strategy | Open Questions or Session Launcher | Important |
| Intelligence layer cost model | Open Questions | Important |
| Project discovery mechanism | Open Questions or Core Entities | Important |
| Minimal CLI for MVP | Phase 1 scope | Important |
| Quick-launch capability in Phase 1 | Phase 1 scope | Important |
| Event retention/pruning policy | New section | Minor |
| Hook resilience (timeouts, buffering) | Hook integration | Minor |
| Chat implementation sketch | Intelligence Layer | Minor |
| Bootstrap plan | New section after Phases | Minor |

---

## Overall Assessment

This is a strong first-draft spec. The vision is clear and differentiated (mission control, not agent orchestration). The responsibility boundary table is excellent and will prevent scope creep. The phasing is mostly sound and the MVP is scoped tightly enough to build in 1-2 focused weeks.

The critical gaps are in the areas that matter most for operational reliability: resource arbitration, process supervision, and crash recovery. These are not architectural rethinks -- they are missing components within an otherwise sound architecture. The runtime adapter interface needs to graduate from a table of one-liners to a real contract before Phase 2 implementation begins.

The biggest risk to "daily driver" status is that Phase 1 without any launch capability is a dashboard you glance at, not a control plane you live in. Pulling in a bare-minimum launch command (no env setup, just spawn-and-watch) would meaningfully increase Phase 1's utility for minimal additional scope.

The spec is ready to implement Phase 1 after addressing the critical items above. Phases 2-5 need the adapter interface and resource management details filled in before implementation begins, but those can be refined iteratively as Phase 1 experience informs the design.
