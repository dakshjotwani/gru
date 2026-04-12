# Gru Design — Discussion Notes

**Date:** 2026-04-11
**Participants:** Daksh, Claude

## Decisions Made

### 1. Backend Language: Go

**Decision:** Go for the backend, TypeScript for the frontend.

**Rationale:**
- Goroutines map perfectly to "one goroutine per session" — session management is the core product
- Process supervision, signal handling, PID management are natural in Go
- Daksh has Go experience
- AI agents (Claude Code) are very proficient at Go
- Enforced simplicity aids long-term readability and maintainability

### 2. API Layer: gRPC

**Decision:** gRPC with protobuf, not REST or Connect.

**Rationale:**
- Strong typed bindings across Go (server) and TypeScript (frontend client)
- First-class streaming support (useful for session event streams)
- `grpcurl` sufficient for debugging (no need for curl-friendly REST)
- Connect was considered but isn't popular enough yet
- Frontend uses grpc-web (proxy or Envoy sidecar, or Buf's connect-web which speaks grpc-web natively)

**Implications:**
- Need a grpc-web proxy for browser clients (or use Buf's `vanguard` / `connect-go` which can serve grpc-web directly from Go)
- CLI client uses native gRPC (no proxy needed)
- `.proto` files become the single source of truth for the API contract
- WebSocket is replaced by gRPC server-streaming for real-time events

### 3. Intelligence Layer: Claude Code Sessions (not API)

**Decision:** Use Claude Code sessions (with Haiku model) for all intelligence work. No direct Claude API key needed.

**Rationale:**
- Daksh doesn't have a Claude API key
- Claude Code sessions with Haiku are sufficient for classification, stuck detection, summaries
- Claude Code gives the intelligence agent access to tools (file reading, bash, etc.) which is useful for knowledge extraction
- Door is open for direct API calls later if a key becomes available

**Implications:**
- Intelligence layer spawns lightweight `claude --model haiku` sessions for classification/scoring
- Summary agent is a longer-running Claude Code session
- Need to manage these "internal" sessions separately from user-facing sessions in the dashboard (or tag them as system sessions)
- Cost is Claude Code usage, not direct API billing

### 4. Port Management: Skip for MVP

**Decision:** No Resource Manager for ports. Setup scripts handle port allocation themselves. If a port is taken, the setup script fails and the agent deals with it.

**Rationale:**
- Gru can't guarantee system-wide port availability anyway
- External apps can grab ports that gru "allocated"
- `max_concurrent_agents` is just a counter — doesn't need a resource manager
- Easy to add later if collisions become a real problem

### 5. Agent Awareness: Skill First, MCP Later

**Decision:** Phase 1-2 uses a `.claude/skills/gru-agent.md` skill loaded into every managed session. Phase 3+ adds a gru MCP server for richer bidirectional communication.

**Rationale:**
- Skill is zero-infrastructure — just a markdown file explaining the protocol
- Hooks provide the outbound channel (agent → gru) for free
- MCP server adds value only when there's knowledge to query (Phase 3+)
- Agents can push status via hook events; MCP lets them pull context from gru

### 6. Session Launching + Attach in Phase 1

**Decision:** Phase 1 includes structured session launching and the ability to attach/view running sessions from the dashboard.

**Rationale:**
- AI agents are building this — no human implementation bottleneck to justify splitting phases
- Session launching is the highest-value feature for daily driving
- Attach = dashboard live event stream + context injection (works across tailnet)
- Terminal attach (`gru attach <id>`) as a nice-to-have

### 7. Event Store: SQLite for Everything

**Decision:** SQLite for events, sessions, insights, knowledge. One store.

**Rationale:**
- 10-20 sessions at ~100 events/sec burst is well within SQLite's capacity (~50k inserts/sec WAL)
- Full SQL queries for dashboard, intelligence layer
- Zero operational overhead
- Migration path to Postgres if needed later
- Go libraries: `modernc.org/sqlite` (pure Go) or `mattn/go-sqlite3`
- Query layer: `sqlc` (generates Go from SQL) recommended

### 8. WebSocket → gRPC Server Streaming

**Decision:** Replace WebSocket with gRPC server-streaming RPCs for real-time events.

**Rationale:**
- gRPC server streaming is first-class and typed
- Same `.proto` definition for request/response and streaming
- grpc-web supports server streaming in browsers
- Protocol: on subscribe, server sends state snapshot then streams incremental events

### 9. Notifications: Service Worker

**Decision:** Use service worker for background desktop notifications.

**Rationale:** Works even when dashboard tab is closed (Chrome). Fallback to browser Notification API for other browsers.

## Open Items Carried Forward

- Hook data richness: research Claude Code hook event payloads in detail before implementation
- Context injection mechanism: file-based vs stdin vs MCP — prototype during Phase 1
- Worktree management strategy: document in Phase 2 spec
- Intelligence cost model: measure actual Claude Code (Haiku) session costs during Phase 2
- Project discovery: `~/.gru/projects.yaml` for MVP
