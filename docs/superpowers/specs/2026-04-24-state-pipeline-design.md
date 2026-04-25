# State Pipeline — Design / Research Spec

**Date:** 2026-04-24
**Status:** Draft (research + design, no implementation)
**Scope:** Reliability of the session-state pipeline that feeds the web UI's session list, priority queue, and push-notification system. Covers ingestion → store → publisher → SSE → frontend. Out of scope: launch UX, attention scoring weights, the underlying tmux/env adapter contract.

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

---

## 3. Recommended direction

**Thesis:** make SQLite the single source of truth for **both** session state and the event feed, give every event a monotonic `seq`, and replace the fire-and-forget publisher with an outbox-tailing informer that speaks SSE-with-`Last-Event-ID` to the browser. Keep the supervisor, but demote it to a *liveness probe + resync trigger,* not an authoritative writer of session status.

### 3.1 Data model

Add to the existing `events` table:

- `seq INTEGER PRIMARY KEY AUTOINCREMENT` — monotonic across the whole DB.
- An index on `(session_id, seq)` for per-session replay and bounded retention scans.

Demote `sessions.status` and `sessions.attention_score` to **derived materialized columns**:

- They are kept up-to-date by the same transaction that inserts the event.
- A periodic (and on-demand) job can rebuild them by folding the event log per session. If they ever disagree with the fold, the fold wins.

Add `sessions.last_event_seq INTEGER` so any reader can answer "is this row caught up to seq N yet?" cheaply.

### 3.2 Writer path (POST /events)

Single transaction, in this order:

1. `INSERT INTO events (...) VALUES (...) RETURNING seq`.
2. `UPDATE sessions SET status = ?, attention_score = ?, last_event_seq = ?, last_event_at = ? WHERE id = ?` — derived from the just-inserted event by a single shared status-derivation function.
3. Commit.
4. Signal the in-process publisher: "new seq available."

Status derivation moves to **one place, server-side** (`internal/state` package or similar). The frontend stops deriving status from event types — it just reads `session.status` from snapshots and updates. This kills the dual-derivation drift class entirely.

Return `202` only **after** commit. (We are on a Mac mini; the per-event commit cost is fine. We are not optimizing for throughput.)

### 3.3 Publisher (informer)

Replace `internal/ingestion/Publisher` with a single goroutine that:

1. Maintains `tail_seq`.
2. On signal *or* after a 250 ms timeout, runs `SELECT id, session_id, type, payload, ts, seq FROM events WHERE seq > ? ORDER BY seq`.
3. Fans out each row to subscribers, advancing `tail_seq` as it goes.
4. **Per-subscriber backpressure: if a subscriber's buffer would overflow, the publisher *closes that subscriber's connection.* It does not drop events.** The client's reconnect-with-`Last-Event-ID` machinery will pick up the slack from the durable log. This is the central reliability win.

### 3.4 Wire protocol (SSE-style stream)

We can keep using gRPC `SubscribeEvents` for now, but the contract changes:

- The request takes an optional `since_seq`.
- The response stream begins with **either** a snapshot (when `since_seq == 0`, or when `since_seq` is older than retention) **or** with replayed events from `seq > since_seq` to current head.
- Each `SessionEvent` carries its `seq`. The client persists the highest `seq` it has seen.
- On reconnect the client sends its last `seq` as `since_seq`. The server resumes losslessly from the outbox.

If we ever drop gRPC for raw SSE for the web client, the same fields map cleanly: `id: <seq>` on each event, `Last-Event-ID` on reconnect.

### 3.5 Snapshot semantics

The current "snapshot then subscribe" race is fixed by reversing the order in the server's stream handler:

1. Read current head `head_seq` from the publisher.
2. Subscribe to the publisher's fan-out, buffered, starting from `head_seq + 1`.
3. Read the snapshot of `sessions` at `head_seq` (transactionally; a `BEGIN; SELECT ...; SELECT MAX(seq) FROM events; COMMIT` to confirm).
4. Send snapshot to the client.
5. Pump live events from the buffered subscription onward.

Equivalent to the K8s `LIST + resourceVersion + WATCH` pattern.

### 3.6 Hook delivery reliability

Replace the fire-and-forget `curl -m 2 &` with:

- **Synchronous** post (Claude Code hooks already block the agent; we are not making that worse — a 2 s timeout is fine).
- **Bounded retry** on connection failure / 5xx (e.g. 3 attempts, exponential backoff, total ≤ 5 s) — Claude Code hook timeouts are higher than this.
- **Idempotency key** — a UUID generated client-side per hook invocation, sent as `X-Gru-Event-ID`. The server `INSERT OR IGNORE`s on this key. This makes retry safe at-least-once → effectively at-most-once-effect.
- **On final failure, write a local fallback file** (`~/.gru/hooks/pending/<id>.json`) and the supervisor (see below) sweeps and re-POSTs.

This is the only place we add net-new server-side machinery: a pending-event sweeper. It costs ~30 lines and closes the silent-loss hole.

### 3.7 Supervisor — narrowed role

The supervisor today writes `sessions.status` directly. **Stop doing that.** It becomes:

1. **Liveness probe.** Walks tmux windows, decides "is the pane alive?" Emits *events* (`session.crash`, `session.killed`) into the same outbox the hook pipeline uses. The state derivation function turns those into status — so there is exactly one writer of `sessions.status`.
2. **Pending-hook sweeper.** Scans `~/.gru/hooks/pending/`, retries posts, deletes on success.
3. **Resync trigger.** On a slow tick (e.g. 5 min), recomputes `attention_score` for all live sessions by folding the event log, and writes if it disagrees with the cached value. This catches any drift.

### 3.8 Frontend

- Remove the duplicate `applyEvent` status switch. Rely on `session.status` from the server.
- On reconnect, replay-style: track highest `seq` seen, send it as `since_seq`. If the server replays events, run them through a *display-only* reducer (counts, ring buffer of recent events). State comes from the snapshot or the latest session-update event.
- Notification trigger keys off the **server-derived** transition, which we add as an explicit `session.transition` event (`from`, `to`) emitted by the writer when status changes. Client doesn't need to diff state itself.
- Make snapshot merges **conditional on `last_event_seq`**: if an incoming snapshot has a lower `last_event_seq` for a session than the client currently holds, ignore it. This kills the stale-snapshot regression class.

### 3.9 Build vs. buy

| Component | Decision | Rationale |
|---|---|---|
| Durable event log | **Build on SQLite** (outbox pattern + `seq`) | We already have SQLite, WAL, and migrations. Adding a column + index is cheaper than introducing JetStream/Redis. |
| Pub/sub fan-out | **Build** (~80 lines, single goroutine, tails outbox) | Trivial at our scale; using a third-party adds operational surface. |
| Reconnect protocol | **Adopt** SSE / `Last-Event-ID` semantics | Standardized, browser does the work; same shape is easy in gRPC stream too. |
| Idempotency / dedupe | **Build** (`INSERT OR IGNORE` on `event_id`) | One unique constraint, no library needed. |
| Backpressure | **Build** (close-on-overflow + replay-on-reconnect) | The whole reliability gain comes from this; can't be outsourced. |
| State derivation | **Build, centralize** | Gru-specific business logic. Just don't write it twice. |
| CDC / WAL tail | **Skip** (`update_hook` is intra-process; cross-process tailing is solved by the outbox poll + signal) | Don't need it. |
| JetStream / Redis Streams | **Skip** | Single-machine, 5–20 sessions. New daemon = new ops cost for zero capability we can't get from SQLite. |
| Temporal | **Skip** | Massive overkill. Borrow the mental model only. |

---

## 4. Anti-patterns (what NOT to do, learned from current breakage)

1. **Don't drop slow subscribers silently.** The current `select { case ch <- evt: default: }` in `Publisher.Publish` is the single biggest source of UI lag. If a buffer overflows, **disconnect the subscriber and let it resume from the durable log.** Never drop and pretend.

2. **Don't make `UpdateSessionLastEvent` failure non-fatal.** Today the handler logs and continues, leaving event-log and session-row inconsistent. Either roll it into one transaction, or treat its failure as a hard 5xx so the hook retries.

3. **Don't fire-and-forget hooks.** `curl -m 2 &` looks cheap but it makes hook delivery probabilistic. The whole pipeline is downstream of this — if it's not reliable, nothing else can be.

4. **Don't snapshot, then subscribe.** The unguarded gap between a snapshot read and the publisher subscription is silent event loss. Always subscribe first, then read the snapshot at a known `seq`, and let the client deduplicate by `seq`.

5. **Don't have two writers of `sessions.status`.** The supervisor and the ingestion handler both mutating `status` is a race we keep paying for. Pick one writer; the other emits *events* and lets the writer derive.

6. **Don't compute the same state machine in two languages.** The Go `switch` in `handler.go:144-173` and the TypeScript `applyEvent` in `useSessionStream.ts` will drift. Derive on the server, ship `status`, render dumb.

7. **Don't merge snapshots unconditionally on the client.** Without a `seq`/`resourceVersion` guard, a stale snapshot regresses correct local state. Ignore snapshots whose `last_event_seq` is older than what the client already has.

8. **Don't rely on the staleness ramp to surface "stuck" sessions.** It's a useful signal, but it's a *symptom detector,* not a recovery mechanism. The pipeline must converge on truth on its own; the ramp should be there only for genuine "Claude is wedged" cases.

9. **Don't trust unbounded in-memory state across restarts.** Today the attention engine's per-session state lives only in process memory. Plan for the server to be killed and restarted at any point; everything that matters has to be reconstructable from the DB.

10. **Don't treat the SSE/gRPC stream as a delivery guarantee.** The stream is a transport, not a queue. The durable log is the queue. Always combine the two with a `seq` resume token.

---

## Open questions

- **Retention.** How long do we keep events? Proposal: 7 days or 100 k rows, whichever is smaller. Old enough that any reasonable client reconnect can replay; bounded enough to keep the DB compact.
- **Schema migration vs. nuke.** Gru is single-machine and we've been comfortable nuking the DB. The outbox + `seq` migration is small enough to do in-place, but nuking is simpler. Default: nuke.
- **gRPC vs. raw SSE.** The browser's free `EventSource` reconnect is attractive. gRPC-web doesn't expose the same auto-reconnect-with-`Last-Event-ID` semantics ergonomically. Worth prototyping both.
- **Per-session attention recomputation cost.** Folding the entire event log for `attention_score` is fine at 5–20 sessions × thousands of events. If this ever becomes hot, snapshot the attention state with a `last_seq` and resume the fold from there.

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
