# Attention Queue + Session Naming — Design Specification

**Date:** 2026-04-12
**Status:** Draft
**Scope:** Spec 1 of 2 — covers session naming, attention queue UI, and tmux input injection. Does NOT cover launch chat, project config, or AI-powered summaries.

---

## Goal

Replace the current session grid with a priority-sorted attention queue that lets the user triage sessions in order of urgency. Sessions get human-readable names and descriptions. The user can take action on sessions directly from the dashboard: approve/deny permission prompts, re-prompt idle agents, or kill sessions.

---

## Data Model Changes

### Proto: `Session` message

Add three fields to `Session` in `proto/gru/v1/gru.proto`:

```protobuf
string name        = 13;  // human-readable, e.g. "auth-frontend-bugfix"
string description  = 14;  // what problem is being solved
string prompt       = 15;  // the initial prompt given to the agent
```

No backwards compatibility concerns — the DB will be nuked on schema change.

### Proto: `LaunchSessionRequest`

Add `name` and `description` fields:

```protobuf
message LaunchSessionRequest {
  string project_dir  = 1;
  string prompt       = 2;
  string profile      = 3;
  string name         = 4;  // required, human-readable session name
  string description  = 5;  // optional, what problem is being solved
}
```

### Proto: New RPC `SendInput`

Add a new RPC for injecting text into a running session's tmux pane:

```protobuf
rpc SendInput(SendInputRequest) returns (SendInputResponse);

message SendInputRequest {
  string session_id = 1;
  string text       = 2;  // text to send via tmux send-keys
}

message SendInputResponse {
  bool success = 1;
}
```

### SQLite schema

Update `001_init.sql` in-place (no migration). Add columns to `sessions`:

```sql
name        TEXT NOT NULL DEFAULT '',
description TEXT NOT NULL DEFAULT '',
prompt      TEXT NOT NULL DEFAULT ''
```

Update sqlc queries to read/write these fields.

---

## Backend Changes

### LaunchSession handler

- Validate `name` is non-empty (return InvalidArgument if missing)
- Store `name`, `description`, and `prompt` in the session record
- Pass `prompt` to the controller as before

### SendInput RPC

New handler in `service.go`:

1. Look up session by ID (404 if not found)
2. Verify session has `tmux_session` and `tmux_window` set (return FailedPrecondition if not)
3. Execute `tmux send-keys -t <tmux_session>:<tmux_window> -- <text> Enter`
4. Return success

The text is sent literally — no escaping beyond what `tmux send-keys` does natively. The frontend is responsible for sending appropriate text (e.g., "y" for approve, "n" for deny, or a full prompt for idle agents).

### CLI changes

`gru launch` gains a required `--name` flag:

```
gru launch --name "auth-bugfix" /path/to/project "fix the auth bug"
```

The positional `prompt` argument and `--name` flag are both required. `--description` is optional.

---

## Frontend Changes

### Remove

- The red "Reconnecting to server..." banner
- Project-grouped session grid layout

### Attention Queue (replaces SessionGrid)

A single sorted list of session cards. Sort order:

1. **needs_attention** — by attention_score descending, then last_event_at descending
2. **running** — by last_event_at descending
3. **idle** — by last_event_at ascending (longest-idle first, these need re-prompting or killing)
4. **starting** — by started_at descending

Terminal sessions (completed, errored, killed) are hidden. A "show completed" toggle can be added later.

### Session Card (collapsed)

Each card shows:

| Element | Source |
|---|---|
| Session name | `session.name` |
| Status badge | Existing `StatusBadge` component |
| Project name | From project lookup |
| Time in state | Computed from `last_event_at` |
| Context preview | See below |

**Context preview** (one line, below the name):

- **needs_attention**: Tool name from the last `notification.needs_attention` event payload (e.g., "Wants to run: Bash")
- **running**: Tool name from the last `tool.pre` event (e.g., "Using: Edit")
- **idle**: "Idle for X minutes"
- **starting**: "Starting..."

### Session Card (expanded)

Clicking a card expands it inline to show:

| Section | Content |
|---|---|
| Description | `session.description` (if set) |
| Initial prompt | `session.prompt` — the task the agent was given |
| Current context | For needs_attention: parsed from the event payload — tool name, tool input summary, notification message. For idle: the message from the last Stop event. |
| Recent events | Last 5 events as a compact list (type + timestamp) |
| Actions | Context-dependent, see below |

### Actions

**needs_attention:**
- **Approve** button — calls `SendInput(session_id, "y")`
- **Deny** button — calls `SendInput(session_id, "n")`
- **Custom response** text input + send button — calls `SendInput(session_id, text)`
- **Attach** button — copies tmux attach command

**idle:**
- **Prompt** text input + send button — calls `SendInput(session_id, text)`
- **Kill** button — calls `KillSession(session_id)`
- **Attach** button — copies tmux attach command

**running:**
- **Kill** button — calls `KillSession(session_id)`
- **Attach** button — copies tmux attach command

**starting:**
- **Kill** button — calls `KillSession(session_id)`

### Event payload parsing

The frontend needs to extract useful context from event payloads. The Claude Code hook payload JSON contains:

```json
{
  "hook_event_name": "Notification",
  "tool_name": "Bash",
  "tool_input": {"command": "rm -rf /tmp/old"},
  "message": "Claude wants to run a command",
  "notification_type": "permission_prompt"
}
```

For display purposes, parse:
- `tool_name` — show as "Wants to use: {tool_name}"
- `tool_input` — show a truncated JSON preview (first 100 chars)
- `message` — show as the context line

This parsing happens in a utility function. Unknown/missing fields are handled gracefully (show "Needs your attention" as fallback).

### Snapshot changes

The `SubscribeEvents` snapshot must include the new `name`, `description`, and `prompt` fields. Since `sessionToJSON` already serializes the full `Session` proto message, this works automatically once the proto and DB are updated.

---

## What's NOT in this spec

- Auto-generated session names from prompt (Spec 2: launch chat)
- AI-powered session summaries or progress tracking (Phase 3: intelligence layer)
- Project configuration, agent profiles, environment setup (Spec 2: launch chat + project config)
- Filtering/search in the attention queue
- "Show completed sessions" toggle
- Desktop notification improvements
