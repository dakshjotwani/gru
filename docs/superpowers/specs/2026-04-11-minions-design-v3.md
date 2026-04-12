# Minions — Design Specification

**Date:** 2026-04-11
**Status:** Draft v3 (post-discussion — Go + gRPC + Claude Code intelligence)

## Vision

Minions is mission control for a fleet of AI coding agent sessions. It watches, informs, alerts, suggests, and launches — but never tells agents how to do their job. The underlying agent runtime (Claude Code, Codex, or future tools) handles execution and internal orchestration. Minions handles visibility, intelligence, and session-level orchestration across projects.

Minions is **runtime-agnostic by design**. Claude Code is the primary runtime, but the interfaces are generic enough to support any agentic tool that can emit events and be launched programmatically.

The long-term goal: you describe what you want done, minions figures out which projects need work, spins up the right agents with the right environments, monitors them, surfaces what needs your attention, accumulates knowledge, and gets smarter over time. "Use minions to build minions."

---

## Core Concepts

### What Minions Is

- A **monitoring layer** that gives you at-a-glance status of all running sessions
- An **intelligence layer** that classifies sessions, detects stuck agents, scores attention priority, and summarizes fleet state
- A **launch platform** that sets up environments and spawns agent sessions from project configs
- A **knowledge accumulator** that learns from agent sessions and generates skills for future agents
- A **notification system** that bubbles up what needs your attention across any frontend

### What Minions Is NOT

- Not an agent orchestration framework — the agent runtime (Claude Code, etc.) handles subagents, agent teams, and internal coordination
- Not a CI/CD system — the agent runtime with its own integrations handles CI failures autonomously
- Not an analytics-only dashboard — it acts (launches, notifies, generates skills), not just reports
- Not coupled to a specific agent runtime — Claude Code is the primary target, but the interfaces support any runtime that can emit events and be launched programmatically

### Responsibility Boundary: Minions vs Agent Runtime

| Concern | Owner |
|---|---|
| Breaking a task into subtasks | Agent runtime (e.g., Claude Code lead agent) |
| Spawning and coordinating subagents | Agent runtime |
| Handling CI failures, retries | Agent runtime (with MCP or built-in integrations) |
| Choosing tools, writing code, running tests | Agent runtime |
| Monitoring all sessions across all projects | **Minions** |
| Classifying session type, detecting stuck agents | **Minions** |
| Setting up environments for new sessions | **Minions** |
| Notifying the human when attention is needed | **Minions** |
| Suggesting what agents to spawn from external context | **Minions** |
| Accumulating knowledge and generating skills | **Minions** |

---

## Core Entities

| Entity | Description | Lifecycle |
|---|---|---|
| **Project** | A codebase with a `.minions/` config directory | Persistent — exists as long as the repo does |
| **Session** | A running agent process via any runtime adapter (Claude Code, Managed Agents, etc.) | Ephemeral — created, runs, terminates |
| **Agent Profile** | A preconfigured way to launch sessions: skills, env scripts, model choice | Persistent — defined in `.minions/config.yaml` |
| **Event** | An emission from any runtime adapter (hooks, SSE, etc.) | Append-only — stored in the backend |
| **Insight** | An AI-derived observation: type classification, stuck detection, attention score | Computed — derived from events |
| **Knowledge Entry** | An accumulated learning from agent sessions | Persistent — grows over time, may graduate to skills |

---

## Architecture

### Approach: Event Bus + Processing Pipeline

Hooks are thin (just HTTP POST raw event JSON). The Go backend ingests, processes, and runs the intelligence layer. Frontends consume a gRPC API (with grpc-web for browsers).

```
┌─────────────────────────────────────────────────────────────────────┐
│                        DATA SOURCES                                 │
│                                                                     │
│  Claude Code hooks ──HTTP POST──┐                                   │
│  (future) Managed Agents SSE ───┤                                   │
│  (future) External MCP ─────────┤                                   │
│         (Slack, Jira, Gmail)     │                                   │
└──────────────────────────────────┼──────────────────────────────────┘
                                   ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     MINIONS BACKEND (Go)                             │
│                                                                     │
│  ┌──────────────┐    ┌─────────────────────────────────────────┐   │
│  │   Event      │    │       Processing Pipeline               │   │
│  │   Ingestion  │───▶│           (goroutines)                  │   │
│  │   (HTTP API) │    │  ┌─ Type Classifier (CC Haiku session)  │   │
│  └──────────────┘    │  ├─ Stuck Detector (timing + CC Haiku)  │   │
│                      │  ├─ Attention Scorer                    │   │
│  ┌──────────────┐    │  ├─ Knowledge Accumulator               │   │
│  │   Session    │◀───│  └─ Summary Agent (CC session)          │   │
│  │   Store      │    └─────────────────────────────────────────┘   │
│  │   (SQLite)   │                                                   │
│  └──────┬───────┘    ┌─────────────────────────────────────────┐   │
│         │            │       Session Launcher                   │   │
│         │            │                                         │   │
│         │            │  Project config loader                  │   │
│         │            │  Environment setup/teardown             │   │
│         │            │  Process supervisor (goroutine per sess) │   │
│         │            └─────────────────────────────────────────┘   │
│         │                                                           │
│         ▼                                                           │
│  ┌──────────────┐                                                   │
│  │  gRPC API    │───▶  Any frontend (grpc-web for browsers,        │
│  │  (protobuf)  │      native gRPC for CLI/mobile/agents)          │
│  └──────────────┘                                                   │
└─────────────────────────────────────────────────────────────────────┘
```

### Agent Runtime Adapter

Minions is runtime-agnostic. The **Agent Runtime Adapter** is the abstraction boundary between minions and the underlying agentic tool. Claude Code is the first implementation; others (Codex, future tools) can be added by implementing the same interface.

The adapter is split into two interfaces that can be implemented independently:

1. **EventNormalizer** — stateless, registered per runtime type. Called during event ingestion to normalize raw events into the common schema. A new runtime only needs this for Phase 1 (monitoring).
2. **SessionController** — stateful, manages launching, controlling, and querying sessions. Needed for Phase 2 (launching).

```go
// --- Event normalization (Phase 1) ---

// EventNormalizer translates runtime-specific events into the common schema.
// Stateless — registered per runtime type.
type EventNormalizer interface {
    RuntimeID() string // "claude-code", "managed-agents", "codex", ...
    Normalize(rawEvent json.RawMessage) (*MinionsEvent, error)
}

type MinionsEvent struct {
    ID        string         `json:"id"`
    SessionID string         `json:"session_id"`
    ProjectID string         `json:"project_id"`
    Runtime   string         `json:"runtime"`
    Type      EventType      `json:"type"`
    Timestamp time.Time      `json:"timestamp"`
    Payload   map[string]any `json:"payload"` // runtime-specific data preserved
}

// Required event types every runtime must emit.
// Optional types (subagent.*) pass through if present.
type EventType string

const (
    EventSessionStart  EventType = "session.start"
    EventSessionEnd    EventType = "session.end"
    EventSessionCrash  EventType = "session.crash"
    EventToolPre       EventType = "tool.pre"
    EventToolPost      EventType = "tool.post"
    EventToolError     EventType = "tool.error"
    EventNotification  EventType = "notification"
    EventSubagentStart EventType = "subagent.start"  // optional
    EventSubagentEnd   EventType = "subagent.end"    // optional
)

// --- Session control (Phase 2) ---

// SessionController manages launching and controlling sessions for a runtime.
type SessionController interface {
    RuntimeID() string
    Capabilities() []Capability
    Launch(ctx context.Context, opts LaunchOptions) (*SessionHandle, error)
}

type Capability string

const (
    CapKill          Capability = "kill"
    CapPause         Capability = "pause"
    CapResume        Capability = "resume"
    CapInjectContext  Capability = "inject_context"
)

type LaunchOptions struct {
    ProjectDir string
    Prompt     string
    Profile    string            // agent profile name from config
    Env        map[string]string // environment variables
}

type SessionHandle struct {
    SessionID string
    PID       int
    PGID      int // process group for clean kill

    // Kill sends SIGTERM to process group, SIGKILL after timeout.
    Kill func(ctx context.Context) error

    // InjectContext sends context to the running agent. Nil if not supported.
    InjectContext func(ctx context.Context, content string) error

    // Done is closed when the process exits. ExitCode is available after.
    Done     <-chan struct{}
    ExitCode func() int
}
```

**Claude Code implementation:**

| Interface | Implementation |
|---|---|
| EventNormalizer | Translates Claude Code hook JSON (SessionStart, PreToolUse, PostToolUse, Notification, etc.) to MinionsEvent |
| SessionController | Spawns `claude` process with `setsid`, passes flags/skills, returns PID/PGID handle |

Each runtime adapter is responsible for translating its native events into the common schema. The processing pipeline, intelligence layer, and frontends only speak the normalized schema.

### Component Breakdown

**Event Ingestion (HTTP API)**
- Receives events from any runtime adapter
- Delegates to the appropriate `EventNormalizer` to produce the common event schema
- Validates, timestamps, associates with session/project
- Writes to event store
- Triggers processing pipeline asynchronously
- Auth: requires pre-shared API key (`Authorization: Bearer <key>`) — see Auth section

**Processing Pipeline**
- Runs asynchronously after event ingestion (doesn't block the runtime)
- Each processor runs in its own goroutine
- Type Classifier: infers session type (research, sw dev, PR review, design, agent team) from conversation context using a Claude Code session (Haiku model)
- Stuck Detector: tracks time since last meaningful progress, compares against historical baselines per task type. Timing heuristics for common cases, Claude Code (Haiku) session for ambiguous situations.
- Attention Scorer: combines signals (blocked, stuck, finished, anomalous behavior) into a priority score
- Knowledge Accumulator: extracts reusable learnings from session events, stores in knowledge base
- Summary Agent: a long-running Claude Code session that periodically synthesizes fleet state (has access to tools, file system, DB queries)
- Intelligence sessions are rate-limited (max 3 concurrent Haiku sessions) and batched where possible
- Door is open for direct Claude API calls later (if API key becomes available) for structured, high-frequency operations

**Session Store (SQLite)**
- Sessions, events, insights, knowledge entries
- Queryable by project, status, type, attention score
- SQLite in WAL mode with retry-on-busy (exponential backoff)
- SQLite for MVP, migration path to Postgres if needed
- Retention: events pruned after 30 days (configurable), session metadata kept 90 days, knowledge entries permanent

**Resource Manager**
- Maintains a resource allocation table in SQLite (ports, session slots per project)
- Provides `acquire(projectId, resources) → Lease` and `release(leaseId)` operations
- Single source of truth for port assignment from the `ports` range in project config
- Rejects launch requests when `max_concurrent_agents` is reached (returns error with queue position)
- Detects and reclaims leases from zombie sessions (sessions whose process is no longer alive)

**Session Launcher**
- Loads project config from `.minions/config.yaml`
- Acquires resources via Resource Manager (ports, session slot)
- Runs environment setup scripts in order; on failure at step N, rollback runs (idempotent — safe to run even if setup never started)
- Healthcheck: polls URL with configurable interval (default 2s), timeout (default 60s), max retries (default 30)
- Template variables in scripts/healthchecks: `{{PORT}}`, `{{PROJECT_DIR}}`, `{{SESSION_ID}}`, `{{WORKTREE_DIR}}`
- Delegates to the appropriate `SessionController` to spawn the agent process
- On normal exit: runs teardown scripts, releases resources
- On crash: Process Supervisor triggers rollback, releases resources

**Process Supervisor**
- Polls running sessions periodically (check PID/PGID liveness, default every 10s)
- On detecting a dead process: runs teardown/rollback, releases resources, updates session status, emits `session.crash` event
- On backend startup: scans sessions marked "running" in DB, verifies PID liveness, reconciles (marks dead sessions, runs cleanup)
- Agents are spawned in their own process group (`setsid`/`detached`) so sessions survive backend restarts
- PID and PGID stored in database for crash recovery
- Kill sends `SIGTERM` to process group, with `SIGKILL` fallback after configurable timeout (default 10s)

**gRPC API (protobuf)**
- `.proto` files are the single source of truth for the API contract
- Unary RPCs: CRUD for projects, sessions, knowledge; query endpoints
- Server-streaming RPCs: real-time session status updates, notifications, events
- Frontend-agnostic: grpc-web for browsers, native gRPC for CLI/mobile/agents
- Debug with `grpcurl`

**Streaming Protocol:**
- `SubscribeEvents` server-streaming RPC: on call, server sends a state snapshot message, then streams incremental events
- Client reconnect: re-calls `SubscribeEvents`, gets a fresh snapshot
- Optional subscription filters: `EventFilter { projects: ["av-sim"], min_attention: 5 }` (MVP: all sessions)
- All messages are protobuf — consistent serialization across the entire API

### Authentication

**MVP:** Pre-shared API key configured in `~/.minions/config.yaml`. Sent as gRPC metadata (`authorization: Bearer <key>`) on all RPCs. Hook scripts include it via environment variable (`MINIONS_API_KEY`). Event ingestion endpoint (HTTP POST from hooks) uses the same key in the `Authorization` header.

**Future:** Tailscale auth integration (identity from WireGuard peer, zero-config on Tailnet).

**Threat model:** Single user, trusted network in MVP. Auth required for multi-machine operation. Kill switch and launch endpoints are gated on auth even in MVP.

### Agent Awareness of Minions

Every agent session managed by minions needs to know it's part of a fleet. Two-phase approach:

**Phase 1-2: Skill-based (zero infrastructure)**
A `.claude/skills/minions-agent.md` skill loaded into every managed session. Explains:
- You are managed by minions mission control
- When to bubble up info vs handle autonomously (structured escalation via Notification hooks)
- How to report status and progress
- When to stop and ask for human input

Agents communicate outbound via existing hook events — no new channel needed.

**Phase 3+: MCP server (richer bidirectional communication)**
Minions runs an MCP server that agents connect to. Adds the ability for agents to *pull* context:
- `minions.reportStatus("implementing auth module, 60% complete")`
- `minions.escalate({tried: [...], failed: "...", needsHuman: "..."})`
- `minions.queryKnowledge("known issues with test DB in this project")`
- `minions.getContext("what are other agents working on in this project?")`

---

## Project Configuration

### Directory Structure

```
.minions/
  config.yaml              # Project config: agent profiles, resources, settings
  skills/                  # Minions-specific operational skills
  env/                     # Setup/teardown/rollback scripts
  helpers/                 # Reusable scripts (worktree helper, port allocator)
  knowledge/               # Accumulated learnings from agent sessions
  candidate-skills/        # AI-drafted skills pending human review

.claude/
  CLAUDE.md                # Project instructions for Claude Code
  skills/                  # Code-oriented skills (coding standards, testing, architecture)
  settings.json            # Claude Code settings
```

### Delineation: `.minions/` vs `.claude/`

| | `.claude/` | `.minions/` |
|---|---|---|
| **Consumed by** | Claude Code (the agent) | Minions (mission control) |
| **Contains** | How to work with this codebase | How to launch and manage agents for this codebase |
| **Examples** | "Use pytest", "Auth uses JWT", "Run `make test`" | "Feature agents need the simulator", "Port pool 8080-8099" |
| **Authorship** | Human-authored + graduated from minions | Human config + AI-accumulated knowledge |

### Knowledge → Skill Graduation Pipeline

```
Agents work and discover things
  → minions accumulates in .minions/knowledge/ (raw learnings)
  → intelligence layer distills into .minions/candidate-skills/ (AI-drafted)
  → human reviews (approve, edit, reject)
  → approved skills graduate to .claude/skills/ or .claude/CLAUDE.md
  → future agents load these skills automatically
  → cycle repeats — minions gets smarter over time
```

### config.yaml Schema

```yaml
project:
  name: av-simulator
  repo: git@github.com:org/av-sim.git

  skills:                              # operational skills for minions
    - ./skills/simulator-ops.md

  environment:
    setup:
      - script: ./env/install-deps.sh
      - script: ./env/start-simulator.sh
      - healthcheck: http://localhost:{{PORT}}/ready
    teardown:
      - script: ./env/stop-simulator.sh
    rollback:
      - script: ./env/rollback.sh       # idempotent

  resources:
    ports: [8080-8099]
    max_concurrent_agents: 4

  runtime: claude-code                  # default runtime for this project
                                         # could also be "managed-agents", "codex", etc.

  agent_profiles:
    feature-dev:
      description: "Implement new features with full simulator access"
      extra_skills: [./skills/feature-workflow.md]
      extra_setup: [./env/start-dev-server.sh]
      model: claude-sonnet-4-6            # runtime-specific; adapter interprets
    bug-fix:
      description: "Debug and fix issues"
      extra_skills: [./skills/debugging.md]
      model: claude-opus-4-6
    pr-review:
      description: "Review pull requests (read-only, no env needed)"
      extra_skills: [./skills/review-checklist.md]
      extra_setup: []                    # no environment needed
      model: claude-sonnet-4-6
    research:
      description: "Research and design work, no code changes"
      extra_skills: []
      extra_setup: []
      model: claude-opus-4-6
```

---

## Intelligence Layer

### Type Classification

Infers session type from conversation context. Runs after the first few events of a session (enough context to classify) and re-evaluates periodically.

**Types:** research, feature-dev, bug-fix, pr-review, design, refactor, agent-team, other

**Input signals:** initial prompt, tools being used, files being touched, skills loaded, agent profile used

**Implementation:** Claude Code session (Haiku model) with a focused prompt. Cached per session, re-evaluated on significant context changes. Lightweight — spawned on demand, short-lived.

### Stuck Detection

Identifies agents that appear to be making no meaningful progress.

**Signals:**
- Time since last tool call (idle too long)
- Repetitive tool calls (same tool, similar inputs — loop behavior)
- Error rate spike (multiple consecutive failures)
- Time on current task vs historical baseline for that task type

**Implementation:** Timing heuristics for the common cases, Claude Code session (Haiku) for ambiguous situations. Configurable thresholds per project/agent profile.

### Attention Scoring

Combines multiple signals into a single priority score that determines notification urgency.

**Signal weights (tunable):**
- Agent blocked/waiting for human input: HIGH (10)
- Agent appears stuck: HIGH (10)
- Agent finished successfully: MEDIUM (5)
- Anomalous behavior (unexpected file edits, scope drift): MEDIUM (5)
- Agent running normally: LOW (1)

**Algorithm:**
- Score = max(active signal weights), not sum — a session can only need so much attention
- Scores decay: multiply by 0.9 per minute since triggering event, with floor of 0
- New events reset the relevant signal's decay timestamp
- Notification threshold: configurable, default 8 (fires on any HIGH signal)

**Output:** Each session gets an attention score. Dashboard sorts by this. Notifications fire when score crosses the threshold.

### Summary Agent

Periodically synthesizes fleet state into a human-readable briefing.

**Triggers:** On-demand (user asks), periodic (configurable — e.g., every 30 minutes), daily digest

**Output:** Natural language summary covering:
- What's running and what stage it's in
- What needs your attention and why
- What completed since last summary
- Cost breakdown
- Suggested next actions

**Delivery:** Dashboard panel, daily digest (Slack/email), response to "chat with minions"

### Knowledge Accumulation

Extracts reusable learnings from session events and outcomes.

**What gets captured:**
- Environment quirks discovered by agents ("test DB needs migration before running")
- Workarounds for known issues ("simulator crashes on scene IDs > 999")
- Effective approaches ("for this repo, always run `make lint` before `make test`")
- Failure patterns ("auth tests are flaky, retry once before escalating")

**Implementation:** After session completion, intelligence layer reviews the session's events and extracts learnings. Stores in `.minions/knowledge/` as structured entries. Periodically distills into candidate skills in `.minions/candidate-skills/`.

### External Context Integration (Phase 5)

Connects to Slack, Atlassian (Jira/Confluence), Gmail via MCP servers to:
- Discover work items (new tickets, Slack requests, email threads)
- Suggest which agents to spawn for which projects
- Provide context for predictive spawning

**Implementation:** MCP server connections from the minions backend. Intelligence layer periodically polls or subscribes to events. Matches work items against project configs to suggest spawn actions.

### Predictive Spawning (Phase 5)

Based on external context and established patterns:
- New PR opened → suggest review agent
- Jira ticket assigned → suggest feature-dev or bug-fix agent
- Slack message mentioning a project → surface for potential agent spawn

**Approval modes:**
- Suggest-only: surfaces in dashboard, human approves
- Auto-spawn with notification: launches automatically, human can kill
- Configurable per trigger type and project

---

## Interfaces

### Web Dashboard (MVP Frontend)

Primary view: session grid organized by project.

**Per-session card:**
- Session name/ID
- Project
- Agent profile / session type (classified)
- Status: running, idle, needs attention, completed, errored
- Attention indicator (color-coded priority)
- Time on current task
- Progress stage (if inferable)
- Quick actions: view details, inject context, kill

**Dashboard sections:**
- Fleet overview: all sessions at a glance, sorted by attention score
- Project view: sessions grouped by project
- Attention queue: only sessions that need you, prioritized
- Chat panel: natural language queries about fleet state
- Summary panel: latest summary agent output

**Notifications:**
- Desktop notifications (browser Notification API + service worker)
- In-dashboard notification center

### Chat with Minions

Natural language interface to fleet state. Available as:
- Dashboard panel (MVP)
- Future: Slack bot, CLI command, mobile app

**Example queries:**
- "What's the status of the auth migration?"
- "How much have I spent today?"
- "What needs my attention?"
- "Spawn a review agent for PR #423 on av-sim"

### Daily Digest

Summary meta-agent's primary output. Delivered to configured channels.

**Contents:**
- What agents accomplished since last digest
- What's currently running and status
- What needs attention
- Cost breakdown
- Candidate skills ready for review
- Suggested work items from external context

### Context Injection

From the dashboard, send context to a running agent without switching terminals.

**Implementation:** Minions writes context to a file or uses Claude Code's input mechanism. The agent picks it up on its next prompt cycle.

### Git Artifact Tracking

Link sessions to their git artifacts in the dashboard.

**Per-session git info:**
- Branch name
- Commits (count, messages)
- PR link (if created)
- Lines changed (+/-)
- Merge status

**Data source:** Hook events (PostToolUse for git operations) + GitHub MCP for PR status.

---

## Incremental Phases

### Phase 1 — MVP: "See Everything" 

**Goal:** A working dashboard that shows all your Claude Code sessions with basic status. Useful enough to be your daily driver while building Phase 2.

**Scope:**
- Hook integration: thin hook scripts that POST events to the backend (with short timeout + background POST to avoid blocking agent)
- Backend: event ingestion API (HTTP for hooks), gRPC API (for frontends/CLI), session store (SQLite), server-streaming for real-time updates, auth (API key)
- Session tracking: start, running, idle, finished, errored
- Project organization: group sessions by project (detected from working directory)
- Project registry: `~/.minions/projects.yaml` listing known project paths
- Basic attention detection: agent waiting for input (from Notification hooks)
- Time on current task: tracked from hook timestamps
- Web dashboard: session grid with status cards, project grouping
- Quick launch: spawn an agent in a directory with a prompt from the dashboard (no env setup — just `claude -p "prompt"`)
- Kill switch: terminate a session via process signal (Claude Code-specific; generalized to runtime adapter in Phase 2)
- Desktop notifications: service worker for background notifications + fallback to browser Notification API
- Minimal CLI: `minions status`, `minions kill <id>`, `minions launch <dir> "prompt"`, `minions tail <id>` (native gRPC client, same Go binary as server)

**NOT in MVP:**
- AI-powered classification (sessions show raw status, not inferred type)
- Stuck detection beyond simple timeout
- External context / MCP integrations
- Knowledge accumulation
- Chat interface
- Full session launching with environment lifecycle (Phase 2 — MVP quick-launch is bare-bones)

**Bootstrap value:** Once Phase 1 is running, you use it to launch and monitor the Claude Code sessions that build Phase 2.

### Phase 2 — Launch: "Do Everything"

**Goal:** A structured way to launch and manage agent sessions from project configs. Major productivity boost — no more manual environment setup.

**Scope:**
- Project configuration (`.minions/config.yaml`)
- Session launcher (env setup, health checks, agent process management, teardown)
- Agent profiles (preconfigured launch templates)
- Environment lifecycle (setup → run → teardown with idempotent rollback)
- Context injection (basic: manual text push to running agent; Phase 3 adds intelligent timing and suggestions)
- Runtime adapter interface (Claude Code first implementation: EventNormalizer + SessionController)
- Resource Manager (port allocation, session slot enforcement)
- Process Supervisor (crash recovery, orphan detection)

**Depends on:** Phase 1 (monitoring — see launched sessions in the dashboard)

### Phase 3 — Intelligence: "Understand Everything"

**Goal:** The AI layer that makes raw events meaningful and enables natural language interaction.

**Scope:**
- Type classification (Claude Code session, Haiku model)
- Stuck detection (timing heuristics + Claude Code Haiku for ambiguous cases)
- Attention scoring (multi-signal priority)
- Summary agent (on-demand + periodic)
- Chat with minions (dashboard panel)
- Natural language spawning ("spawn a review agent for PR #423") — builds on Phase 2 launcher
- Git artifact tracking
- Progress stage inference

**Depends on:** Phase 1 (needs event data), Phase 2 (launcher for NL spawning)

### Phase 4 — Learn: "Get Smarter Over Time"

**Goal:** The knowledge flywheel.

**Scope:**
- Knowledge accumulation from completed sessions
- Candidate skill generation
- Graduation pipeline (knowledge → candidate skill → human review → .claude/ skill)
- Outcome tracking (did the PR merge? was it reverted?)
- Performance tracking (which configs work best for which tasks)
- Daily digest

**Depends on:** Phase 3 (needs session lifecycle data and outcomes)

### Phase 5 — Reach Out: "Know What's Coming"

**Goal:** External context drives proactive spawning.

**Scope:**
- Slack MCP integration
- Atlassian (Jira) MCP integration
- Gmail MCP integration
- Work item discovery and matching to projects
- Predictive spawning (suggest or auto-spawn with approval)

**Depends on:** Phase 2 (needs launcher), Phase 3 (needs intelligence for matching)

---

## Tech Stack

| Component | Choice | Rationale |
|---|---|---|
| Backend runtime | **Go** | Goroutines map perfectly to session management (one goroutine per session). Excellent process supervision, signal handling, PID management. Enforced simplicity aids readability. AI agents are very proficient. |
| API layer | **gRPC (protobuf)** | Typed codegen for Go server + TypeScript client. First-class server streaming for real-time events. `grpcurl` for debugging. grpc-web for browser clients. |
| Data store | **SQLite** (`modernc.org/sqlite` pure Go) in WAL mode | Zero-config, embedded, ~50k inserts/sec. `sqlc` for type-safe query generation. Retry-on-busy with exponential backoff. Migration path to Postgres if needed. |
| Frontend | **React + TypeScript + Vite** | Well-known, fast dev cycle, good dashboard ecosystem. gRPC client via `@connectrpc/connect-web` or `grpc-web`. |
| Intelligence | **Claude Code sessions (Haiku)** | No API key needed. Spawns lightweight `claude --model haiku` sessions for classification/scoring. Summary agent is a longer-running CC session with tool access. Door open for direct API calls later. |
| Hook scripts | **Shell (bash/sh)** | Minimal — `curl POST` with 2s timeout, backgrounded to avoid blocking the agent |
| Desktop notifications | **Service worker** (background) + browser Notification API (fallback) | Service worker works even with tab closed |
| Process management | **`setsid` + `os/exec`** | Agents in own process group; PID/PGID persisted to DB for crash recovery |
| CLI | **Go** (same binary as server) | `minions status`, `minions launch`, `minions kill`, `minions tail` — native gRPC client, no proxy needed |

---

## Open Questions

1. **Hook data richness:** How much context do Claude Code hooks provide? Do we get enough from hook events to classify session type, or do we need to supplement with log file reading?
2. **Context injection mechanism:** What's the best way to send context to a running Claude Code session? File-based? Stdin? MCP?
3. **Multi-machine coordination:** When agents run on different machines in the tailnet, how does the hook → backend communication work? Just HTTP POST to a known minions backend URL?
4. **Session state persistence:** If minions backend restarts, can it reconstruct session state from hooks that fire after restart, or do we need to persist enough to survive restarts? (v2: Process Supervisor reconciles on startup by checking PID liveness against DB state)
5. **Cost tracking:** Can we get token usage data from Claude Code hooks, or do we need another data source?
6. **Worktree management:** Does minions manage git worktrees for concurrent sessions on the same repo? If so, how are they allocated, named, and cleaned up? Suggested: Session Launcher creates `minions-<session-id>` worktree from configured base branch; teardown removes it (or preserves for review if commits exist).
7. **Intelligence layer cost model:** What is the expected cost of running Claude Code (Haiku) sessions for the intelligence layer with 10-20 concurrent user sessions? Strategies: short-lived Haiku sessions, batching classifications, reusing summary agent session. Measure actual costs during Phase 3 development. Door open for direct API calls later if API key becomes available.
8. **Project discovery:** How does minions know about projects? MVP: manual registry in `~/.minions/projects.yaml`. Later: `minions init` command + auto-discovery from git repos in configured directories.

---

## Bootstrap Plan

How "use minions to build minions" works in practice:

1. **Build Phase 1 manually** — one Claude Code session, no minions yet
2. **Deploy Phase 1 locally** — start using it as your daily driver for monitoring
3. **Build Phase 2** — launch 2-3 Claude Code sessions from the Phase 1 dashboard (quick-launch); monitor them in real-time
4. **Deploy Phase 2** — now you have structured launching with env setup
5. **Build Phase 3+** — use Phase 2's launcher + agent profiles to spawn sessions for Phase 3 development; the intelligence layer from Phase 3 makes building Phase 4+ faster
6. **Flywheel spins** — knowledge accumulated from building minions itself becomes skills that improve future development sessions
