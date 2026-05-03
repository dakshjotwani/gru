# Rev-3: hook-driven event log

**Decision.** Replace the rev-2 transcript-tailer + notify-file architecture with a single gru-owned per-session JSONL log (`~/.gru/events/<sid>.jsonl`) written by `gru hook ingest` — invoked from Claude Code hooks, the supervisor, and gru's own CLI/RPC handlers. The tailer reads only this log; Claude's transcript JSONL is no longer folded into state.

## Context

Rev-1 (HTTP hooks → gru server) was rejected for retry-logic complexity, network coupling, and the firehose volume of `PostToolUse`. Rev-2 replaced it with: Claude writes its own transcript JSONL (we tail), our hooks write to `~/.gru/notify/<sid>.jsonl` (we tail), the supervisor writes a third file (we tail). Three input streams, three formats, one fold function switching on a `Source` enum. ADR-0001 records one of the seams that broke under that design.

In rev-2 we discovered:

- The notify file's transcript_path field caused cross-session pollution (today's debug session).
- The `Source` enum leaks format details — `state.NotificationTranscriptPath` is a second parser of the same shape.
- The supervisor file was pure in-process IPC routed through disk; we collapsed it to a Go channel, but that re-introduced the durability gap rev-2's file gave us.
- Tailing Claude's transcript means tracking Anthropic's evolving JSONL grammar (`isSidechain`, `attachment`, `file-history-snapshot`, `last-prompt`, etc.) — most of which we discard.

## Why rev-3 over rev-2

- **Single source of truth.** Tailer reads one log, in gru's grammar, with a `version` field per line for schema evolution. The `Source` enum dies; `state.Derive` switches on a small typed event set we own.
- **Anti-corruption layer.** Claude's hook payloads are translated to gru events by `gru hook ingest`. Future hook-schema changes are absorbed in one place — the translator — not in the fold or projection.
- **Durable supervisor + CLI.** Both the supervisor goroutine and gRPC handlers like `KillSession` invoke the same ingest path, so every status-affecting signal lands in the durable log. `sessions.status` becomes a write-only column for the tailer; nothing else mutates it.
- **No transcript schema dependency.** Anthropic-format parsing leaves the fold path entirely. Frontend dashboards that want to show conversation content read the transcript file directly, on demand, without folding.

## Why not rev-1 again

The rev-2 spec rejected hook → gru because of network calls + retries + volume. Rev-3's hook does *file appends*, not network calls. POSIX guarantees atomic appends ≤ PIPE_BUF; no retry logic needed. Volume cost reduces to "spawn `gru` binary per hook fire," which is a static-linked Go binary doing open-append-close — sub-millisecond and bounded.

## Consequences

- **`--resume` becomes invisible.** A `claude --resume <gru-sid>` inside the gru pane mints a new Claude session_id; the sibling-Claude guard in `gru hook ingest` rejects hook payloads whose `stdin.session_id` doesn't match the cwd-pinned gru id. Status freezes at last-known. Locked in: gru does not support `--resume`; users should `gru launch` a fresh session and reference history via the prompt.
- **Hook completeness becomes load-bearing.** Status correctness depends on Claude firing every hook we care about. Mitigation: a thin transcript heartbeat (mtime-only stat, no derivation) flags drift if the transcript grew without a hook firing. Logging only; not a state source.
- **`PermissionMode` and `last-prompt` decoration fields move out of state.** They're not in hook payloads. The dashboard either drops them or fetches them from the transcript on demand.
- **Single-writer property for `sessions.status`.** Only the tailer writes status. `service.KillSession` no longer calls `UpdateSessionStatus` directly — it appends a `killed_by_user` event and lets derivation flip the row.
- **`~/.gru/supervisor/` file is gone (rev-2 cleanup).** `~/.gru/notify/` is replaced by `~/.gru/events/`. ADR-0001's transcript-path-discovery problem self-resolves: the log is keyed by gru session id at write time, not discovered.

## Considered alternatives

- *Keep tailing Claude's transcript JSONL for status*: rejected — re-introduces the schema-coupling and `Source` enum we want to delete.
- *Write hook events directly to SQLite from the hook process*: rejected — couples the hook binary to the gru DB schema; JSONL with a per-line `version` field gives us a stable contract that survives migrations.
- *Bash hook script*: rejected — today's debug session showed validation logic split between bash and Go drifts; a single Go subcommand owns it.
- *In-memory channel only (current rev-2.5 supervisor path)*: rejected — loses events on server crash; the durable file is the whole point of rev-2's offline-resilience contract, and rev-3 honors it for every source, not just transcripts.
