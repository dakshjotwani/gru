# State Pipeline — Design / Research Spec

**Date:** 2026-04-24 (rev 2: 2026-04-25)
**Status:** Draft (research + design, no implementation)
**Scope:** Reliability of the session-state pipeline that feeds the web UI's session list, priority queue, and push-notification system. Covers ingestion → store → publisher → SSE → frontend. Out of scope: launch UX, attention scoring weights, the underlying tmux/env adapter contract.

**Revision 2 (2026-04-25):** Rewrote §3 to lead with a **transcript-tailer** architecture after discovering that Claude Code already maintains a per-session, real-time, append-only event log at `~/.claude/projects/<hash>/<session-id>.jsonl`. That file gives us the same properties we were going to build (durable append-only log, monotonic position via byte offset, replay-on-reconnect) with no new infrastructure. The previous SQLite-outbox design from rev 1 is preserved as a rejected alternative. §1 audit and §2 reference systems are unchanged; §4 anti-patterns are lightly edited so they still apply to the new direction.

---

## Problem

Operators can't trust what the dashboard shows. Two failure modes recur:

1. **Lag.** A real transition (e.g. a session becoming `needs_attention`) takes many seconds to appear, or only appears after the user reloads the tab.
2. **Stuck.** A session is shown in a state that no longer reflects reality and never self-corrects until the operator forces something — typically a reconnect or a `gru` CLI inspection.

Both modes break the two highest-value UI features:

- **Priority queue** — sorts by `attentionScore`. If scores are stale or wrong, the queue points the operator at the wrong session.
- **Push notifications** — fire on the client-side state-machine transition into `needs_attention`. Missed transitions = silent drops; spurious ones = alert fatigue.

The pipeline today has at least one place where each of "loss," "reorder," "stale," and "unconditional overwrite" is possible. We need to redesign it to make those failures impossible by construction, not by adding more code paths to hope they don't.

---

## 1. Current architecture audit

### 1.1 End-to-end trace

```
[Claude Code in tmux pane]
   │  hooks/claude-code.sh  (curl -m 2  &  — fire-and-forget)
   ▼
[POST /events]
   │  internal/ingestion/handler.go  (writes event, derives status, scores attention, publishes)
   ▼
[SQLite]                        [In-memory Publisher]
 events table                    map[id]chan SessionEvent
 sessions row (status,           non-blocking send;
   last_event_at,                drops slow subscribers
   attention_score)              silently
                                       │
                                       ▼
[gRPC SubscribeEvents stream]   internal/server/service.go
 1) snapshot of all sessions
 2) then subscribe to publisher
                                       │
                                       ▼
[Web client]                    web/src/hooks/useSessionStream.ts
 merges snapshots + deltas;
 derives status independently;
 fires OS notifications

[Supervisor goroutine, every ~10–15s]
 internal/supervisor/supervisor.go
 inspects tmux panes, may write status independently
```

This is a **dual-writer** system (ingestion handler + supervisor both mutate `sessions.status`) with a **lossy fan-out** (in-memory publisher) and a **dual derivation** (status switch lives in both Go and TS).

### 1.2 Hop-by-hop failure modes

| Hop | What can go wrong | Where | Effect |
|---|---|---|---|
| Hook script | `curl -m 2 ... &` is fire-and-forget; no retry, no failure surfacing | `hooks/claude-code.sh:46-51` | Silent event loss when server is restarting, slow, or briefly unreachable. Session state never advances on the server side. |
| Hook script | Session-ID resolution fails → `exit 0` | `hooks/claude-code.sh:33` | Same: silent drop, no log. |
| Ingestion | Unknown runtime → 400 | `handler.go:96` | One-off, unlikely. |
| Ingestion | Unknown event type from normalizer → 422 | `internal/adapter/claude/normalizer.go` | Adding a new Claude Code hook upstream silently breaks the pipeline until we ship a normalizer change. |
| Ingestion | `CreateEvent` succeeds but `UpdateSessionLastEvent` fails — non-fatal, just logged | `handler.go:189-198` | **Event row exists, session row is stale.** The UI's snapshot reflects sessions, not the event log; session view goes wrong and stays wrong. |
| Ingestion | 202 returned before WAL fsync | `handler.go:211` | Crash between accept and checkpoint loses events that the hook believed were durable. |
| Publisher | Non-blocking send drops to slow subscribers | `handler.go:42-52` | Live transitions silently dropped to any subscriber whose channel buffer (size 64 in service) is momentarily full. No metric, no log. |
| Publisher | No replay, no sequence number | `handler.go:20-52` | A subscriber that disconnects for 200 ms has no way to ask "what did I miss?" |
| Status derivation | Identical `switch` lives in both Go (`handler.go:144-173`) and TS (`useSessionStream.ts` applyEvent) | duplication | Easy to drift. Adding a state to one side without the other produces silent UI/backend disagreement. |
| Attention engine | In-memory state; recompute driven by supervisor tick | `internal/attention/engine.go`; `supervisor.go` | If the supervisor tick is delayed or missed, `staleness` ramp doesn't advance, score is wrong. |
| Attention engine | Score persisted only via the same `UpdateSessionLastEvent` that can fail | `handler.go:189-198` | Failure leaves DB score stale and there's no compensating write. |
| Supervisor | Inspects tmux panes; may overwrite `sessions.status` | `supervisor.go` reconcile loop | Races the hook pipeline with no per-session ordering. A late-arriving hook can clobber a freshly correct supervisor write — or vice versa. |
| SubscribeEvents | Sends snapshot **before** subscribing to publisher | `internal/server/service.go` (snapshot then `pub.Subscribe`) | Events between the SELECT for the snapshot and the publisher subscription are lost to that client, with no way to detect the gap. |
| SubscribeEvents | No resume token, no `last_event_id` | service.go | Reconnect refetches the snapshot. If a transition happened entirely in the offline window, the client sees the *current* state but **never sees the transition** — so the client-side notification trigger never fires. |
| Frontend | Snapshot overwrites local map unconditionally | `useSessionStream.ts` snapshot handler | A late or stale snapshot can regress state that the client already had correct. |
| Frontend | Notification fires only on `prev !== NEEDS_ATTENTION → NEEDS_ATTENTION` | `useSessionStream.ts` | A session that bounces idle → needs_attention several times generates one notification, then silence until it leaves the state. |

### 1.3 Why state "gets stuck"

The compounding pattern is:

1. A hook fires, `curl -m 2` times out (server doing GC, sqlite checkpoint, anything ≥ 2 s).
2. Event is lost. DB never reflects the transition.
3. The publisher had nothing to publish, so connected clients see no change either.
4. The supervisor doesn't run a "compare panes-to-DB-and-correct" pass for hook-derivable state — its job is liveness, not state truth — so it doesn't repair the divergence.
5. The attention engine's staleness ramp slowly ticks the score up, eventually surfacing the session as "stuck" — but with no semantic state attached. The UI shows the *old* status (`running`) until the next event delivers (which may never come; e.g. if Claude is sitting idle waiting for a permission prompt and we lost the `needs_attention` event, no further hooks will fire).

Stated bluntly: **the system has no mechanism to recover a missed event.** Recovery is incidental — we hope the next event will overwrite the wrong state, but if "the next event" was the one we lost, we never converge.

### 1.4 Why state "lags"

The publisher's non-blocking, drop-on-slow-subscriber design means the per-client channel buffer is the only smoothing mechanism. Any time the client is rendering, GC'ing, or just slow to drain (a tab in the background), a burst of three or four events can overflow the buffer and we drop *exactly the events we needed to deliver in order.* The client never knows; it sees an unbroken stream that's missing the middle.

In practice the operator perceives this as "I clicked over and the state was old, then it caught up after I clicked another tab" — that's the snapshot rebuild on something else triggering an update.

---

## 2. Reference systems

We surveyed six widely-deployed designs. Three are good fits for our scale; three are useful for ideas-only.

### 2.1 Kubernetes shared informers — *strong fit (concept)*

Each watcher does `LIST` to seed a local cache plus a `resourceVersion`, then opens a long-lived `WATCH` from that version. Server returns events in resource-version order. If the server drops the watcher (`410 Gone` because the requested version aged out), the client re-LISTs and resyncs. Periodic full resyncs catch silent divergence.

What to steal:

- A **monotonic resource version** (we'd use a SQLite `seq INTEGER PRIMARY KEY AUTOINCREMENT`).
- The **list+watch protocol**: snapshot at version V, then stream all events with seq > V.
- **Periodic full resync** as a belt-and-suspenders against bugs we haven't found yet.
- One shared in-process informer feeding many UI subscribers (we already have this shape).

What to skip: actual etcd, the entire CRD/apiserver stack, the work-queue + reconciler controller pattern (it's there for distributed controllers, not single-host UI fan-out).

Sources:
- https://github.com/kubernetes/sample-controller/blob/master/docs/controller-client-go.md
- https://deepwiki.com/openshift/kubernetes-client-go/5.1-sharedinformer-and-informerfactory

### 2.2 SQLite outbox / CDC — *strong fit (mechanism)*

Write the business state and an `events` row in the same transaction. A relay tails the `events` table on a monotonic `seq` column, optionally awoken by an in-process signal from the writer. The store is the source of truth; the publisher is a derived view.

What to steal:

- **Transactional outbox** so "session row updated" and "event durable in feed" are atomic. No more "event stored, session row stale, oh well."
- **`seq INTEGER PRIMARY KEY AUTOINCREMENT`** as our `resourceVersion`.
- **Polling tail with an in-process wakeup channel** — the writer signals "new seq" and the publisher does a single `SELECT WHERE seq > last`. Polling falls back to ~250 ms on missed wakeups.
- **Bounded retention** (e.g. last 24 h or 100 k rows) so reconnect-replay is cheap.

Notes: `sqlite3_update_hook` is in-process only and doesn't survive across DB connections, so we don't rely on it. WAL-mode + a signal channel + `seq` index is plenty fast for 5–20 sessions.

Sources:
- https://sqlite.org/c3ref/update_hook.html
- https://dev.to/actor-dev/implementing-the-outbox-pattern-with-sqlite-and-using-brighter-15ha
- https://turso.tech/blog/introducing-change-data-capture-in-turso-sqlite-rewrite

### 2.3 Server-Sent Events with `Last-Event-ID` — *strong fit (wire)*

The HTML5 `EventSource` spec defines an auto-reconnect loop: on disconnect the browser waits, reconnects, and sends `Last-Event-ID: <last seen>` as a request header. The server is expected to backfill events with greater IDs.

What to steal:

- Use **SSE (or a JSON streaming gRPC RPC that adopts the same semantics)** as the wire protocol.
- On reconnect with `Last-Event-ID: N`, the server **`SELECT * FROM events WHERE seq > N ORDER BY seq` and replays before going live.** This is the same drain-then-switch handshake Redis Streams' PEL gives you.
- Set `id:` on every emitted event so the browser caches it for free.

Sources:
- https://html.spec.whatwg.org/multipage/server-sent-events.html
- https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events

### 2.4 NATS JetStream — *technically fine, infrastructurally heavy*

Subjects + persistent streams + durable consumers + ack semantics + producer dedupe (`Nats-Msg-Id`). Single static Go binary, can be embedded.

Why not for Gru: introduces a second source of truth alongside SQLite, brings dual-write bugs unless we fully cut over, and pays for cluster features we will never use. Worth borrowing the **Msg-Id dedupe window** as a concept, but not the daemon.

### 2.5 Redis Streams — *clean reconnect, wrong infra footprint*

`XADD` + consumer groups + Pending Entries List (PEL). The PEL replay-then-`>` handshake is the cleanest at-least-once-with-resume design in the industry.

Why not for Gru: adds Redis as a dependency that does nothing for us that SQLite can't already do. We **mimic the PEL handshake in SQL** instead of running Redis.

### 2.6 Temporal / Cadence — *idea only*

State as a deterministic fold over an immutable event history; explicit event-schema versioning ("patches"). Massively overkill operationally.

What to steal: the **mental model** — `session.status` and `attention_score` should be a fold over the ordered event log, not a separately-mutated row that we hope agrees with the events. If the row is just a cache, we can rebuild it any time we suspect it's wrong.

Source: https://docs.temporal.io/workflow-execution/event

### 2.7 Claude Code's per-session transcript JSONL — *the closest fit, and it's already running*

Each Claude Code session continuously writes a real-time, append-only JSONL transcript to `~/.claude/projects/<project-hash>/<session-id>.jsonl`, with the path also handed to any configured `statusLine` script as `transcript_path`. Inspection of a live session's file (~7 MB, thousands of lines) shows the following entry types:

| `type` (and `subtype` where present) | What it tells us |
|---|---|
| `assistant` (carries `message.stop_reason`: `end_turn` / `tool_use`) | Turn boundaries — `end_turn` ≈ idle, `tool_use` ≈ mid-tool |
| `user` (with `tool_use_id` for tool results) | Tool result returned, pairs with the assistant `tool_use` |
| `system` / `stop_hook_summary` | Every Stop-hook invocation, with `command`, `durationMs`, `hookErrors`, `preventedContinuation` — including Gru's own hook script's executions |
| `system` / `turn_duration` | Per-turn timing |
| `system` / `compact_boundary` | Context-compaction events |
| `system` / `informational` | Misc system messages |
| `permission-mode` | Current permission mode (`default` / `acceptEdits` / `plan` / ...) |
| `last-prompt` | Most recent user prompt verbatim |
| `file-history-snapshot`, `worktree-state`, `attachment`, `queue-operation` | Misc context Claude tracks |

**Properties:**

- **Durable by construction.** Claude flushes lines as events complete. Independent of any network call we'd make.
- **Append-only with stable byte offsets.** The byte offset *is* a per-session resource version: equivalent to K8s `resourceVersion` (§2.1), Redis `last-delivered-id` (§2.5), and SSE `Last-Event-ID` (§2.6) — for free.
- **Naturally resumable.** Restart the server, re-`lseek` to the last-known offset, replay forward.
- **Per-session totally ordered.** No global ordering across sessions, but we don't need one — the UI is already a session-keyed view.
- **No backpressure / loss surface.** Nothing between Claude and disk that can drop events.
- **Documented for external consumption.** The `transcript_path` is part of the published `statusLine` contract.

**The one gap** — explicit "blocked on permission prompt" signals are *not* in the transcript. The closest signal is "assistant turn ended with `stop_reason: tool_use` and the matching `user` tool-result has not yet arrived," but that's also the signature of a long-running tool. The `Notification` hook is the only definitive source for permission prompts.

**Footprint:** zero new processes, zero new dependencies. The producer (Claude Code) is already writing this file whether we tail it or not.

**Fit for Gru:** dominant. The K8s + outbox + SSE design from §2.1–§2.6 was a sketch of what we'd have built to provide these properties. They're already provided.

Sources:
- https://code.claude.com/docs/en/statusline
- https://gist.github.com/AKCodez/ffb420ba6a7662b5c3dda2edce7783de (community field reference)
- https://github.com/withLinda/claude-JSONL-browser (community transcript parser)

---

## 3. Recommended direction

**Thesis:** Stop trying to build a durable event log and pretend Gru is the producer. **Claude Code is already producing one — the per-session JSONL transcript.** Make Gru a *tailer* of those files. The hook → HTTP → outbox → publisher chain reduces to: tailer goroutine per session → SQLite (derived state only) → existing pub/sub → frontend. The byte offset of each transcript is the per-session resume token.

There is one capability the JSONL doesn't expose — explicit "blocked on permission" notifications — so we keep exactly **one** hook (`Notification`), and we wire it through a *local file* rather than HTTP. End state: zero in-band network calls in the producer path. The producer can't lose events because it's not making any.

### 3.1 Architecture

```
┌──────────────────────────────────────────────────────────┐
│  Claude Code (in tmux pane)                              │
│   ├─ writes ~/.claude/projects/<hash>/<sid>.jsonl        │ ◀── source of truth (durable, append-only)
│   └─ Notification hook → appends ~/.gru/notify/<sid>.jsonl│ ◀── only signal not in transcript
└────────────────────────┬─────────────────────────────────┘
                         │ fsnotify (or 250 ms poll fallback)
                         ▼
┌──────────────────────────────────────────────────────────┐
│  Gru server                                              │
│   ├─ per-session Tailer goroutine                        │
│   │     parses new JSONL lines from last byte offset,    │
│   │     applies state-derivation, writes to SQLite       │
│   │     (sessions row + recent-events projection),       │
│   │     signals publisher                                │
│   ├─ Publisher: tails SQLite event projection by seq     │
│   ├─ Supervisor: tmux liveness probe (no status writes)  │
│   └─ gRPC SubscribeEvents: snapshot+stream with seq      │
└────────────────────────┬─────────────────────────────────┘
                         ▼
                     React client
```

### 3.2 The Tailer (per-session goroutine)

For each non-terminal session, one goroutine:

1. Looks up `transcript_path` and `transcript_offset` from the `sessions` row (these are persisted, see §3.3). On first launch of a session, `transcript_path` is resolved at session start by mapping `cwd` to Claude's project-hash directory and listing `*.jsonl` for the session's `claude_session_id`.
2. Opens the file, `lseek`s to `transcript_offset`.
3. Reads forward, parsing complete JSON lines (a partial trailing line is buffered until the next read).
4. For each parsed line:
   - Runs the **state-derivation function** (§3.4) which returns a tuple `(maybe_new_status, maybe_attention_delta, maybe_projected_event)`.
   - In a single SQLite transaction: insert the projected event row (if any), update `sessions(status, attention_score, last_event_at, transcript_offset, claude_stop_reason, permission_mode)` to the new values.
   - On commit, signals the publisher.
5. Watches the file via `fsnotify`. If `fsnotify` is unavailable (rare), falls back to a 250 ms poll of `os.Stat`.
6. Also reads `~/.gru/notify/<sid>.jsonl` (the permission hook's local file) as a second input source — same pattern, separate offset.

A *single* control goroutine starts/stops these on session create/terminate (`SessionStart`/`SessionEnd`/`SessionCrash` derived state), and on Gru server startup re-spawns one per non-terminal row in `sessions`.

**Rotation / compaction.** Claude does not rotate transcript files within a session (compaction is recorded inline as a `compact_boundary` line). If a session ID changes, that's a new session and a new file; nothing to handle. Defensive: if `Stat()` shows the file shrunk below `transcript_offset`, log loudly and re-derive from offset 0 — should never happen in practice.

### 3.3 SQLite — what stays, what goes

**Stays:** the `sessions` table, but with two new columns:

- `transcript_path TEXT NOT NULL` — full path to Claude's JSONL file
- `transcript_offset INTEGER NOT NULL DEFAULT 0` — byte offset of the next unread line
- `notify_offset INTEGER NOT NULL DEFAULT 0` — same for the per-session notification file
- `claude_stop_reason TEXT` — last seen `assistant.stop_reason` (`end_turn`, `tool_use`, etc.)
- `permission_mode TEXT` — last seen `permission-mode` value

**Stays, but downgraded:** an `events` table, retained as a **derived projection** for the UI's recent-events ring buffer (per-session). The tailer writes one row per *interesting* JSONL line — we ignore noise like `file-history-snapshot`. The events table is no longer authoritative; it can be dropped and rebuilt from the JSONL files at any time.

- Add `seq INTEGER PRIMARY KEY AUTOINCREMENT` for the publisher's tail.
- Add a unique constraint on `(session_id, transcript_offset)` so the tailer is idempotent if it crashes mid-batch.

**Goes:** the `/events` HTTP endpoint, the in-memory attention engine's reliance on hook-call timing, the `gru-hook.sh` curl post.

### 3.4 State derivation — one place, server-side

A pure Go function, e.g. `internal/state/derive.go`:

```go
func DeriveFromTranscriptLine(prev SessionState, line []byte) (next SessionState, projected *Event)
```

Cases (sketch, not exhaustive):

- `assistant` with `stop_reason == "end_turn"` → `status = idle`
- `assistant` with `stop_reason == "tool_use"` → `status = running`, remember the tool_use_id(s)
- `user` with matching `tool_use_id` → tool resolved; if all outstanding tool_use_ids resolved, status follows the next `assistant` line
- `system / stop_hook_summary` → idle confirmation (and exposes Stop hook diagnostics for free)
- `permission-mode` → update `permission_mode`; if it transitioned to `plan` from `default`, that's a useful UI signal
- `last-prompt` → just record it for the dashboard's "what was this agent told to do" surface

Plus a *second* function consuming the notification file:

- Notification of `permission_prompt` → `status = needs_attention`, set `attention_score` to the configured "blocked on permission" weight. A subsequent `assistant` `tool_use` resolution clears it.

The frontend never re-derives status. It reads `sessions.status` and renders.

### 3.5 The one residual hook: Notification → local file

Replace the entire `gru-hook.sh` (which currently fires for all events) with a tiny script that runs **only** on the `Notification` hook event and **only writes to a local file**:

```bash
#!/bin/bash
# Append the hook payload to a per-session JSONL file. No network.
SID=$(jq -r .session_id < "$1")  # or read via env / .gru/session-id
mkdir -p "$HOME/.gru/notify"
cat "$1" >> "$HOME/.gru/notify/$SID.jsonl"
```

Atomic-append on POSIX; the tailer reads it byte-offset-tracked, exactly like the transcript. **No retry logic needed because there is no failure mode.** If Gru is down, the file simply grows; on next start the tailer catches up.

This collapses the entire hook-reliability subproblem to a `>>` redirect.

### 3.6 Wire protocol

`SubscribeEvents` keeps its current shape but its semantics tighten:

- Request takes `since_seq` (server-wide event seq, from the events projection table).
- Response begins with a **snapshot of `sessions`** at the current head seq, then streams events with `seq > head_seq`. The snapshot carries each session's `last_event_seq` so the client can detect which deltas it has already applied.
- On reconnect the client sends its last seen `seq`. Server replays everything since.
- Per-subscriber backpressure: **on overflow, close the connection.** The client reconnects with its last seen `seq` and the server replays from the events projection.

If we drop gRPC for raw SSE later, the same shape maps onto `id:` lines + `Last-Event-ID`. No protocol change required.

### 3.7 Snapshot semantics

Same fix as before — subscribe before snapshotting, with the snapshot pinned to `head_seq`. K8s `LIST + WATCH`, just with the events projection as the watch target instead of an outbox.

### 3.8 Supervisor — narrowed further

The supervisor's role shrinks again:

1. **Tmux liveness probe.** Walks tmux windows. When a pane disappears, writes `claude_pid_exit` event into the events projection — derivation function turns that into `status = errored` or `status = killed` based on context.
2. **Tailer health watchdog.** If a Tailer goroutine has not advanced its offset in N minutes despite the file growing, restart it.
3. **No status writes.** Status is exclusively the tailer's output.
4. **Attention score recompute.** On a slow tick (5 min), folds the per-session events projection to recompute `attention_score`; corrects drift if any.

The pending-hook sweeper from rev 1 is gone — there are no pending hooks.

### 3.9 Frontend

Largely unchanged from rev 1's prescription:

- Drop the client-side `applyEvent` status switch. Trust `session.status` from the server.
- Track highest `seq` seen; resume on reconnect via `since_seq`.
- Notifications fire on server-emitted `session.transition` events (`from` → `to`), not on diff-the-map.
- Snapshot merges are guarded by `last_event_seq` to prevent stale-snapshot regressions.

### 3.10 Build vs. buy

| Component | Decision | Rationale |
|---|---|---|
| Durable per-session event log | **Reuse Claude's JSONL transcript** | Already exists, already durable, already real-time. We were going to build SQLite-backed equivalent — don't. |
| Permission-prompt signal | **Build** (one Notification hook, local-file append) | Not in the transcript. Trivially simple — `>>` redirect. |
| Tailer | **Build** (per-session goroutine, fsnotify + offset tracking) | ~150 lines. Standard fare. |
| Pub/sub fan-out | **Build** (single goroutine, tails events projection by `seq`) | Same as rev 1; same simplicity. |
| Reconnect protocol | **Adopt** SSE / `Last-Event-ID` semantics | Same as rev 1. |
| Backpressure | **Build** (close-on-overflow + replay) | Same as rev 1; the central reliability win. |
| State derivation | **Build, centralize, server-side** | Gru-specific business logic; one source of truth. |
| Outbox table | **Skip** | Replaced by Claude's per-session JSONL. The `events` table survives only as a UI projection. |
| HTTP `/events` endpoint | **Delete** | Producer no longer makes network calls (except to its own filesystem). |
| `gru-hook.sh` (multi-event) | **Replace** with tiny Notification-only local-file script | Single hook, no curl, no retry. |
| JetStream / Redis / Temporal | **Skip** | Same as rev 1. |

Net code surface vs. today: smaller. We delete the `/events` ingestion handler and most of `gru-hook.sh`; we add one tailer package and a derivation function. The publisher and frontend changes are the same in either design.

### 3.11 Rejected alternative — SQLite outbox + retried HTTP hooks (rev 1)

The rev-1 design proposed: keep the hook → HTTP → ingestion handler path, but tighten it (synchronous post, idempotency keys, retries, local fallback file for sweeping), make a single SQL transaction write both the event and the derived state, give every event a monotonic `seq` (turning the events table into a transactional outbox), and replace the in-memory publisher with an outbox-tailing informer.

**Why rejected as the primary design:** it builds, in Gru, all the properties Claude Code already provides per-session for free. The seq column duplicates what byte offsets already are; the outbox duplicates what the JSONL already is; the retry/idempotency machinery exists only because we made the producer call us over the network. Removing the network removes the entire failure class — and removing the failure class removes the machinery built to defend against it. The transcript-tailer design is strictly smaller and strictly more robust.

**What rev 1 still gets right and we keep:** single state writer, server-side state derivation, snapshot-after-subscribe with a resource version, close-on-overflow backpressure with reconnect-replay, snapshot regression guard via `last_event_seq`, supervisor demoted away from authoritative writes, anti-pattern list. Most of §3 in rev 1 was correct in *spirit* — it just identified the wrong substrate.

**When rev 1 would beat the tailer design:** if Anthropic ever changes the transcript format incompatibly without notice, or removes/relocates `transcript_path` from the statusLine contract, the tailer stops working. The outbox design is independent of Claude's internals. We accept the format-coupling risk on the bet that (a) it's a published contract and (b) re-targeting the tailer to a new format is days of work, not a foundational redo.

---

## 4. Anti-patterns (what NOT to do, learned from current breakage)

1. **Don't drop slow subscribers silently.** The current `select { case ch <- evt: default: }` in `Publisher.Publish` is the single biggest source of UI lag. If a buffer overflows, **disconnect the subscriber and let it resume from the durable log.** Never drop and pretend.

2. **Don't have a "non-fatal" write that affects state correctness.** Today's `UpdateSessionLastEvent` failure is logged and ignored, leaving event-log and session-row inconsistent. In the new design the same temptation will appear in the tailer's commit path — resist it. Any write that determines what the UI sees must be all-or-nothing: succeed and advance the offset, or fail and retry from the same offset.

3. **Don't put the producer on the network.** `curl -m 2 &` looks cheap but it makes hook delivery probabilistic and gives you a whole subsystem (retries, idempotency keys, sweeper) just to fight the failure mode you introduced. The new design has the producer write to its own filesystem and the consumer tail it; nothing in between can fail.

4. **Don't snapshot, then subscribe.** The unguarded gap between a snapshot read and the publisher subscription is silent event loss. Always subscribe first, then read the snapshot at a known `seq`, and let the client deduplicate by `seq`.

5. **Don't have two writers of `sessions.status`.** The supervisor and the ingestion handler both mutating `status` is a race we keep paying for. Pick one writer; the other emits *events* and lets the writer derive.

6. **Don't compute the same state machine in two languages.** The Go `switch` in `handler.go:144-173` and the TypeScript `applyEvent` in `useSessionStream.ts` will drift. Derive on the server, ship `status`, render dumb.

7. **Don't merge snapshots unconditionally on the client.** Without a `seq`/`resourceVersion` guard, a stale snapshot regresses correct local state. Ignore snapshots whose `last_event_seq` is older than what the client already has.

8. **Don't rely on the staleness ramp to surface "stuck" sessions.** It's a useful signal, but it's a *symptom detector,* not a recovery mechanism. The pipeline must converge on truth on its own; the ramp should be there only for genuine "Claude is wedged" cases.

9. **Don't trust unbounded in-memory state across restarts.** Today the attention engine's per-session state lives only in process memory. Plan for the server to be killed and restarted at any point; everything that matters has to be reconstructable from the DB.

10. **Don't treat the SSE/gRPC stream as a delivery guarantee.** The stream is a transport, not a queue. The durable log is the queue. Always combine the two with a `seq` resume token.

11. **Don't re-derive state on the client.** Both the rev-1 and rev-2 designs centralize state derivation server-side for the same reason: any time the same finite-state machine lives in two languages, it drifts. Render dumb.

12. **Don't tail without persisting the offset transactionally with the state update.** If the offset advances but the state write fails, you've silently dropped an event — the failure mode that motivates this whole spec, just relocated. One transaction, both writes, or neither.

---

## Open questions

- **Resolving `transcript_path` at session launch.** We need to map a session's `cwd` (and Claude's session ID) to the project-hash directory under `~/.claude/projects/`. Worth a 30-line probe to confirm Anthropic's hashing scheme is stable; alternatives are running a one-shot statusLine script ourselves to ask Claude for the path (the contract publishes it), or watching `~/.claude/projects/` for new files.
- **fsnotify on macOS.** The `fsnotify` Go library uses `kqueue` on Darwin. Reliability across editor save patterns isn't a concern for us (Claude is the only writer), but worth a brief soak test. Polling fallback at 250 ms is a sufficient backstop for 20 sessions.
- **Detecting a stuck Claude process.** With no hooks firing and the JSONL not growing, "Claude is wedged" looks identical to "Claude is idle." The supervisor's tmux-pane probe + the existing staleness ramp cover this; no new mechanism needed, but we should confirm the heuristic still feels right after the rewrite.
- **Backfill on first run / migration.** When we deploy the tailer for an existing session that's already been running, do we re-derive from offset 0 or trust whatever's currently in `sessions`? Default: re-derive, since the JSONL is canonical and the session row may be wrong (which is the whole point).
- **Events-projection retention.** The events table is now derived. Proposal: keep last 24 h or 10 k rows per session. Older state can be re-derived from the JSONL on demand if a UI ever wants deep history.
- **gRPC vs. raw SSE.** Same question as rev 1 — `EventSource` reconnect-with-`Last-Event-ID` is free in the browser; gRPC-web is awkward for that. Worth prototyping.

## Out of scope

- Tuning the attention scoring weights themselves. This spec is about delivering *whatever* score we compute reliably.
- Replacing tmux as the launch substrate. The Env adapter (v2 design) is orthogonal.
- Multi-host or multi-operator. Gru remains single-machine.

---

## References

- Kubernetes sample-controller (client-go architecture): https://github.com/kubernetes/sample-controller/blob/master/docs/controller-client-go.md
- DeepWiki — SharedInformer and InformerFactory: https://deepwiki.com/openshift/kubernetes-client-go/5.1-sharedinformer-and-informerfactory
- NATS JetStream consumers: https://docs.nats.io/nats-concepts/jetstream/consumers
- Redis Streams overview: https://redis.io/docs/latest/develop/data-types/streams/
- Redis XREADGROUP: https://redis.io/docs/latest/commands/xreadgroup/
- SQLite `update_hook`: https://sqlite.org/c3ref/update_hook.html
- Transactional outbox with SQLite (Brighter): https://dev.to/actor-dev/implementing-the-outbox-pattern-with-sqlite-and-using-brighter-15ha
- Turso CDC for SQLite: https://turso.tech/blog/introducing-change-data-capture-in-turso-sqlite-rewrite
- Temporal — Events and Event History: https://docs.temporal.io/workflow-execution/event
- WHATWG HTML, Server-sent events: https://html.spec.whatwg.org/multipage/server-sent-events.html
- MDN — Using server-sent events: https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events
- Claude Code — Customize your status line (publishes `transcript_path`): https://code.claude.com/docs/en/statusline
- Claude Code statusline JSON field reference (community gist): https://gist.github.com/AKCodez/ffb420ba6a7662b5c3dda2edce7783de
- Claude Code JSONL transcript browser (community parser, format reference): https://github.com/withLinda/claude-JSONL-browser
- fsnotify (Go cross-platform file events): https://github.com/fsnotify/fsnotify
