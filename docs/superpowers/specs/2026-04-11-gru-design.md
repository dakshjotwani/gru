# Gru — Design Specification

**Date:** 2026-04-11
**Status:** Draft

## Vision

Gru is mission control for a fleet of AI coding agent sessions. It watches, informs, alerts, suggests, and launches — but never tells agents how to do their job. The underlying agent runtime (Claude Code, Codex, or future tools) handles execution and internal orchestration. Gru handles visibility, intelligence, and session-level orchestration across projects.

Gru is **runtime-agnostic by design**. Claude Code is the primary runtime, but the interfaces are generic enough to support any agentic tool that can emit events and be launched programmatically.

The long-term goal: you describe what you want done, gru figures out which projects need work, spins up the right agents with the right environments, monitors them, surfaces what needs your attention, accumulates knowledge, and gets smarter over time. "Use gru to build gru."

---

## Core Concepts

### What Gru Is

- A **monitoring layer** that gives you at-a-glance status of all running sessions
- An **intelligence layer** that classifies sessions, detects stuck agents, scores attention priority, and summarizes fleet state
- A **launch platform** that sets up environments and spawns agent sessions from project configs
- A **knowledge accumulator** that learns from agent sessions and generates skills for future agents
- A **notification system** that bubbles up what needs your attention across any frontend

### What Gru Is NOT

- Not an agent orchestration framework — the agent runtime (Claude Code, etc.) handles subagents, agent teams, and internal coordination
- Not a CI/CD system — the agent runtime with its own integrations handles CI failures autonomously
- Not an analytics-only dashboard — it acts (launches, notifies, generates skills), not just reports
- Not coupled to a specific agent runtime — Claude Code is the primary target, but the interfaces support any runtime that can emit events and be launched programmatically

### Responsibility Boundary: Gru vs Agent Runtime

| Concern | Owner |
|---|---|
| Breaking a task into subtasks | Agent runtime (e.g., Claude Code lead agent) |
| Spawning and coordinating subagents | Agent runtime |
| Handling CI failures, retries | Agent runtime (with MCP or built-in integrations) |
| Choosing tools, writing code, running tests | Agent runtime |
| Monitoring all sessions across all projects | **Gru** |
| Classifying session type, detecting stuck agents | **Gru** |
| Setting up environments for new sessions | **Gru** |
| Notifying the human when attention is needed | **Gru** |
| Suggesting what agents to spawn from external context | **Gru** |
| Accumulating knowledge and generating skills | **Gru** |

---

## Core Entities

| Entity | Description | Lifecycle |
|---|---|---|
| **Project** | A codebase with a `.gru/` config directory | Persistent — exists as long as the repo does |
| **Session** | A running agent process via any runtime adapter (Claude Code, Managed Agents, etc.) | Ephemeral — created, runs, terminates |
| **Agent Profile** | A preconfigured way to launch sessions: skills, env scripts, model choice | Persistent — defined in `.gru/config.yaml` |
| **Event** | An emission from any runtime adapter (hooks, SSE, etc.) | Append-only — stored in the backend |
| **Insight** | An AI-derived observation: type classification, stuck detection, attention score | Computed — derived from events |
| **Knowledge Entry** | An accumulated learning from agent sessions | Persistent — grows over time, may graduate to skills |

---

## Architecture

### Approach: Event Bus + Processing Pipeline

Hooks are thin (just HTTP POST raw event JSON). The backend ingests, processes, and runs the intelligence layer. Frontends consume a WebSocket/API.

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
│                     MINIONS BACKEND                                  │
│                                                                     │
│  ┌──────────────┐    ┌─────────────────────────────────────────┐   │
│  │   Event      │    │       Processing Pipeline               │   │
│  │   Ingestion  │───▶│                                         │   │
│  │   (HTTP API) │    │  ┌─ Type Classifier (Claude API)        │   │
│  └──────────────┘    │  ├─ Stuck Detector (timing + AI)        │   │
│                      │  ├─ Attention Scorer                    │   │
│  ┌──────────────┐    │  ├─ Knowledge Accumulator               │   │
│  │   Session    │◀───│  └─ Summary Agent (periodic)            │   │
│  │   Store      │    └─────────────────────────────────────────┘   │
│  │   (SQLite)   │                                                   │
│  └──────┬───────┘    ┌─────────────────────────────────────────┐   │
│         │            │       Session Launcher                   │   │
│         │            │                                         │   │
│         │            │  Project config loader                  │   │
│         │            │  Environment setup/teardown             │   │
│         │            │  Claude Code process management         │   │
│         │            └─────────────────────────────────────────┘   │
│         │                                                           │
│         ▼                                                           │
│  ┌──────────────┐                                                   │
│  │  WebSocket   │───▶  Any frontend                                 │
│  │  + REST API  │                                                   │
│  └──────────────┘                                                   │
└─────────────────────────────────────────────────────────────────────┘
```

### Agent Runtime Adapter

Gru is runtime-agnostic. The **Agent Runtime Adapter** is the abstraction boundary between gru and the underlying agentic tool. Claude Code is the first implementation; others (Codex, future tools) can be added by implementing the same interface.

**The adapter interface has four concerns:**

| Concern | What it does | Claude Code implementation |
|---|---|---|
| **Event Source** | Emit session events to gru | Hooks (shell scripts) → HTTP POST |
| **Session Launcher** | Start a new agent session | Spawn `claude` process with flags/skills |
| **Session Control** | Kill, pause, inject context | Process signals, stdin, file-based |
| **Session Metadata** | Expose runtime-specific info | Session ID, working dir, model, subagent tree |

**Normalized event schema:** Regardless of the runtime, events are normalized to a common schema before entering the processing pipeline:

```typescript
interface GruEvent {
  id: string;
  sessionId: string;
  projectId: string;
  runtime: string;           // "claude-code" | "managed-agents" | "codex" | ...
  type: string;              // normalized: "session.start" | "tool.pre" | "tool.post" | ...
  timestamp: number;
  payload: Record<string, unknown>;  // runtime-specific data preserved
}
```

Each runtime adapter is responsible for translating its native events into this schema. The processing pipeline, intelligence layer, and frontends only speak the normalized schema.

### Component Breakdown

**Event Ingestion (HTTP API)**
- Receives events from any runtime adapter
- Normalizes to common event schema
- Validates, timestamps, associates with session/project
- Writes to event store
- Triggers processing pipeline

**Processing Pipeline**
- Runs asynchronously after event ingestion (doesn't block the runtime)
- Type Classifier: infers session type (research, sw dev, PR review, design, agent team) from conversation context using Claude API
- Stuck Detector: tracks time since last meaningful progress, compares against historical baselines per task type
- Attention Scorer: combines signals (blocked, stuck, finished, anomalous behavior) into a priority score
- Knowledge Accumulator: extracts reusable learnings from session events, stores in knowledge base
- Summary Agent: periodically synthesizes fleet state into a human-readable briefing

**Session Store (SQLite)**
- Sessions, events, insights, knowledge entries
- Queryable by project, status, type, attention score
- SQLite for MVP, migration path to Postgres if needed

**Session Launcher**
- Loads project config from `.gru/config.yaml`
- Runs environment setup scripts
- Waits for health checks
- Delegates to the appropriate runtime adapter to spawn the agent process
- Manages teardown (including idempotent rollback on failure)

**WebSocket + REST API**
- REST: CRUD for projects, sessions, knowledge; query endpoints
- WebSocket: real-time session status updates, notifications, events
- Frontend-agnostic: web, CLI, mobile, tmux can all connect

---

## Project Configuration

### Directory Structure

```
.gru/
  config.yaml              # Project config: agent profiles, resources, settings
  skills/                  # Gru-specific operational skills
  env/                     # Setup/teardown/rollback scripts
  helpers/                 # Reusable scripts (worktree helper, port allocator)
  knowledge/               # Accumulated learnings from agent sessions
  candidate-skills/        # AI-drafted skills pending human review

.claude/
  CLAUDE.md                # Project instructions for Claude Code
  skills/                  # Code-oriented skills (coding standards, testing, architecture)
  settings.json            # Claude Code settings
```

### Delineation: `.gru/` vs `.claude/`

| | `.claude/` | `.gru/` |
|---|---|---|
| **Consumed by** | Claude Code (the agent) | Gru (mission control) |
| **Contains** | How to work with this codebase | How to launch and manage agents for this codebase |
| **Examples** | "Use pytest", "Auth uses JWT", "Run `make test`" | "Feature agents need the simulator", "Port pool 8080-8099" |
| **Authorship** | Human-authored + graduated from gru | Human config + AI-accumulated knowledge |

### Knowledge → Skill Graduation Pipeline

```
Agents work and discover things
  → gru accumulates in .gru/knowledge/ (raw learnings)
  → intelligence layer distills into .gru/candidate-skills/ (AI-drafted)
  → human reviews (approve, edit, reject)
  → approved skills graduate to .claude/skills/ or .claude/CLAUDE.md
  → future agents load these skills automatically
  → cycle repeats — gru gets smarter over time
```

### config.yaml Schema

```yaml
project:
  name: av-simulator
  repo: git@github.com:org/av-sim.git

  skills:                              # operational skills for gru
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

**Implementation:** Claude API call with a focused prompt. Cached per session, re-evaluated on significant context changes. Uses Haiku for cost efficiency.

### Stuck Detection

Identifies agents that appear to be making no meaningful progress.

**Signals:**
- Time since last tool call (idle too long)
- Repetitive tool calls (same tool, similar inputs — loop behavior)
- Error rate spike (multiple consecutive failures)
- Time on current task vs historical baseline for that task type

**Implementation:** Timing heuristics for the common cases, Claude API call for ambiguous situations. Configurable thresholds per project/agent profile.

### Attention Scoring

Combines multiple signals into a single priority score that determines notification urgency.

**Signal weights (tunable):**
- Agent blocked/waiting for human input: HIGH
- Agent appears stuck: HIGH
- Agent finished successfully: MEDIUM
- Anomalous behavior (unexpected file edits, scope drift): MEDIUM
- Agent running normally: LOW

**Output:** Each session gets an attention score. Dashboard sorts by this. Notifications fire when score crosses configurable thresholds.

### Summary Agent

Periodically synthesizes fleet state into a human-readable briefing.

**Triggers:** On-demand (user asks), periodic (configurable — e.g., every 30 minutes), daily digest

**Output:** Natural language summary covering:
- What's running and what stage it's in
- What needs your attention and why
- What completed since last summary
- Cost breakdown
- Suggested next actions

**Delivery:** Dashboard panel, daily digest (Slack/email), response to "chat with gru"

### Knowledge Accumulation

Extracts reusable learnings from session events and outcomes.

**What gets captured:**
- Environment quirks discovered by agents ("test DB needs migration before running")
- Workarounds for known issues ("simulator crashes on scene IDs > 999")
- Effective approaches ("for this repo, always run `make lint` before `make test`")
- Failure patterns ("auth tests are flaky, retry once before escalating")

**Implementation:** After session completion, intelligence layer reviews the session's events and extracts learnings. Stores in `.gru/knowledge/` as structured entries. Periodically distills into candidate skills in `.gru/candidate-skills/`.

### External Context Integration (Phase 5)

Connects to Slack, Atlassian (Jira/Confluence), Gmail via MCP servers to:
- Discover work items (new tickets, Slack requests, email threads)
- Suggest which agents to spawn for which projects
- Provide context for predictive spawning

**Implementation:** MCP server connections from the gru backend. Intelligence layer periodically polls or subscribes to events. Matches work items against project configs to suggest spawn actions.

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

### Chat with Gru

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

**Implementation:** Gru writes context to a file or uses Claude Code's input mechanism. The agent picks it up on its next prompt cycle.

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
- Hook integration: thin hook scripts that POST events to the backend
- Backend: event ingestion API, session store (SQLite), WebSocket for real-time updates
- Session tracking: start, running, idle, finished, errored
- Project organization: group sessions by project (detected from working directory)
- Basic attention detection: agent waiting for input (from Notification hooks)
- Time on current task: tracked from hook timestamps
- Web dashboard: session grid with status cards, project grouping
- Desktop notifications: browser Notification API for attention-needed
- Kill switch: terminate a session from the dashboard

**NOT in MVP:**
- AI-powered classification (sessions show raw status, not inferred type)
- Stuck detection beyond simple timeout
- External context / MCP integrations
- Knowledge accumulation
- Chat interface
- Session launching (you still launch `claude` manually; gru just watches)

**Bootstrap value:** Once Phase 1 is running, you can use gru to monitor the Claude Code sessions that are building Phase 2.

### Phase 2 — Launch: "Do Everything"

**Goal:** A structured way to launch and manage agent sessions from project configs. Major productivity boost — no more manual environment setup.

**Scope:**
- Project configuration (`.gru/config.yaml`)
- Session launcher (env setup, health checks, agent process management, teardown)
- Agent profiles (preconfigured launch templates)
- Environment lifecycle (setup → run → teardown with idempotent rollback)
- Context injection (send context to running agents from dashboard)
- Runtime adapter interface (Claude Code first implementation)

**Depends on:** Phase 1 (monitoring — see launched sessions in the dashboard)

### Phase 3 — Intelligence: "Understand Everything"

**Goal:** The AI layer that makes raw events meaningful and enables natural language interaction.

**Scope:**
- Type classification (Claude API, Haiku)
- Stuck detection (timing heuristics + AI for ambiguous cases)
- Attention scoring (multi-signal priority)
- Summary agent (on-demand + periodic)
- Chat with gru (dashboard panel)
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

**Depends on:** Phase 3 (needs launcher), Phase 2 (needs intelligence for matching)

---

## Tech Stack (Recommended)

| Component | Choice | Rationale |
|---|---|---|
| Backend runtime | TypeScript + Bun | Fast, good WebSocket support, same language as frontend, Anthropic SDK available |
| Data store | SQLite (bun:sqlite) | Zero-config, embedded, good enough for single-user 10-20 sessions. Migration path to Postgres if needed. |
| Frontend | React + Vite | Well-known, fast dev cycle, good ecosystem for dashboards |
| Real-time | WebSocket (native Bun) | Built-in, no extra deps |
| AI calls | Anthropic SDK (TypeScript) | Direct integration, prompt caching for repeated patterns |
| Hook scripts | Shell (bash/sh) | Minimal — just `curl POST` the event JSON |
| Desktop notifications | Browser Notification API | Works cross-platform, no native deps for MVP |
| Process management | Node child_process / Bun shell | For launching `claude` processes in Phase 3 |

---

## Open Questions

1. **Hook data richness:** How much context do Claude Code hooks provide? Do we get enough from hook events to classify session type, or do we need to supplement with log file reading?
2. **Context injection mechanism:** What's the best way to send context to a running Claude Code session? File-based? Stdin? MCP?
3. **Multi-machine coordination:** When agents run on different machines in the tailnet, how does the hook → backend communication work? Just HTTP POST to a known gru backend URL?
4. **Session state persistence:** If gru backend restarts, can it reconstruct session state from hooks that fire after restart, or do we need to persist enough to survive restarts?
5. **Cost tracking:** Can we get token usage data from Claude Code hooks, or do we need another data source?
