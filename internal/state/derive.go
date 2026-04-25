// Package state implements the per-session state-derivation function:
// a deterministic fold over Claude Code's per-session JSONL transcript
// (and the local notification file) into Gru's session row + projected
// event log. Lives entirely in pure-Go space — no I/O, no globals.
//
// Design contract: same input lines → same output state. That property
// is what lets the tailer wipe the events projection and replay from
// byte 0 on every server start (see spec §3.2).
package state

import (
	"encoding/json"
	"strings"
	"time"
)

// Status mirrors the textual session status used in the SQLite sessions
// row and in protobuf SessionStatus. Kept as a string to match the
// existing column without conversion.
type Status string

const (
	StatusStarting       Status = "starting"
	StatusRunning        Status = "running"
	StatusIdle           Status = "idle"
	StatusNeedsAttention Status = "needs_attention"
	StatusCompleted      Status = "completed"
	StatusErrored        Status = "errored"
	StatusKilled         Status = "killed"
)

// Source identifies which input stream produced a line. The notification
// file emits a different shape than the transcript JSONL, so the
// derivation function uses Source to dispatch.
type Source int

const (
	// SourceTranscript is a line from ~/.claude/projects/<hash>/<sid>.jsonl.
	SourceTranscript Source = iota
	// SourceNotification is a line from ~/.gru/notify/<sid>.jsonl
	// (the residual permission hook's append target).
	SourceNotification
	// SourceSupervisor is an in-band synthetic line emitted by the
	// supervisor when a tmux pane disappears. Carries a "claude_pid_exit"
	// signal that maps to errored/completed.
	SourceSupervisor
)

// State captures everything the derivation function needs to remember
// across lines. It is *not* what we persist — the tailer projects this
// into the sessions row + events row on commit.
type State struct {
	Status           Status
	AttentionScore   float64
	ClaudeStopReason string
	PermissionMode   string

	// pendingToolUseIDs tracks assistant tool_use blocks that have not yet
	// been resolved by a matching user tool_result. While any are
	// pending, status stays running. This is the "is Claude inside a
	// tool call right now?" signal.
	PendingToolUseIDs map[string]struct{}

	// LastEventAt is the timestamp of the most recent line that produced
	// a projected event. RFC3339 string to match the sessions column
	// without a parse round-trip.
	LastEventAt string
}

// Initial returns the state for a freshly-launched session — what the
// tailer starts with before reading any lines.
func Initial() State {
	return State{
		Status:            StatusStarting,
		AttentionScore:    1.0,
		PendingToolUseIDs: map[string]struct{}{},
	}
}

// Projected is one row the derivation function asks the tailer to write
// into the events projection. Empty Type means "no row" — the line was
// noise (file-history-snapshot, attachment, etc.) and should be ignored.
type Projected struct {
	Type      string
	Timestamp string // RFC3339; empty → tailer fills with time.Now().UTC()
	Payload   []byte // raw JSON line, or a small synthesized payload
}

// Derive applies one line to prev and returns the resulting state plus
// (optionally) one projected event row. The function is pure: same
// inputs always produce the same outputs.
func Derive(prev State, src Source, line []byte) (State, *Projected) {
	// Defensive: copy the pending-tool-use set so we never alias the
	// caller's map. The tailer reuses State across calls; mutating the
	// caller's map here would silently corrupt their view if they
	// retained the previous state for a snapshot.
	next := prev
	next.PendingToolUseIDs = make(map[string]struct{}, len(prev.PendingToolUseIDs))
	for k := range prev.PendingToolUseIDs {
		next.PendingToolUseIDs[k] = struct{}{}
	}

	switch src {
	case SourceTranscript:
		return deriveTranscript(next, line)
	case SourceNotification:
		return deriveNotification(next, line)
	case SourceSupervisor:
		return deriveSupervisor(next, line)
	default:
		return next, nil
	}
}

// transcriptLine is the minimum shape we care about across all the
// JSONL entry types Claude emits. Everything else is ignored.
type transcriptLine struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype,omitempty"`
	PermissionMode string          `json:"permissionMode,omitempty"`
	Timestamp      string          `json:"timestamp,omitempty"`
	Message        json.RawMessage `json:"message,omitempty"`
	ToolUseResult  json.RawMessage `json:"toolUseResult,omitempty"`
}

// assistantMessage is the inner shape of `message` when type=assistant.
// Contains stop_reason and a content array that may include tool_use
// blocks (which we track to know whether Claude is mid-tool).
type assistantMessage struct {
	StopReason string                 `json:"stop_reason"`
	Content    []assistantContentItem `json:"content"`
}

type assistantContentItem struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// userMessage is the inner shape of `message` when type=user. The
// content array carries tool_result blocks whose tool_use_id matches a
// previously seen assistant tool_use.
type userMessage struct {
	Content json.RawMessage `json:"content"`
}

type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id,omitempty"`
}

func deriveTranscript(next State, line []byte) (State, *Projected) {
	var l transcriptLine
	if err := json.Unmarshal(line, &l); err != nil || l.Type == "" {
		// Malformed or untyped — silently skip. Same behavior we'd want
		// if Claude added a new entry shape we don't recognize: don't
		// mutate state, don't emit a row.
		return next, nil
	}

	switch l.Type {
	case "assistant":
		var m assistantMessage
		if len(l.Message) > 0 {
			_ = json.Unmarshal(l.Message, &m)
		}
		// Track outstanding tool_use IDs so a later user tool_result can
		// resolve them. If stop_reason=tool_use, Claude is mid-tool and
		// status is running; if end_turn, status is idle.
		for _, item := range m.Content {
			if item.Type == "tool_use" && item.ID != "" {
				next.PendingToolUseIDs[item.ID] = struct{}{}
			}
		}
		next.ClaudeStopReason = m.StopReason
		switch m.StopReason {
		case "end_turn":
			// Turn fully complete; only flip to idle if no tools are
			// outstanding (defensive — Claude shouldn't emit end_turn
			// with pending tool_use blocks, but we don't trust it).
			if len(next.PendingToolUseIDs) == 0 {
				next.Status = StatusIdle
			} else {
				next.Status = StatusRunning
			}
		case "tool_use":
			next.Status = StatusRunning
		case "stop_sequence", "max_tokens", "refusal":
			// These are rare but explicit Claude stop reasons; treat as
			// idle so the UI doesn't get stuck on running.
			next.Status = StatusIdle
		default:
			// Unknown stop_reason — keep current status.
		}
		next.LastEventAt = l.Timestamp
		return next, &Projected{Type: "assistant.message", Timestamp: l.Timestamp, Payload: line}

	case "user":
		// Resolve any tool_use ids whose tool_result is now in.
		var m userMessage
		if len(l.Message) > 0 {
			_ = json.Unmarshal(l.Message, &m)
		}
		// content can be a string (plain user prompt) or an array of
		// blocks (tool_result, text, etc.). Try array shape first.
		var blocks []toolResultBlock
		if err := json.Unmarshal(m.Content, &blocks); err == nil {
			for _, b := range blocks {
				if b.Type == "tool_result" && b.ToolUseID != "" {
					delete(next.PendingToolUseIDs, b.ToolUseID)
				}
			}
		}
		next.LastEventAt = l.Timestamp
		// If all pending tool_use ids resolved AND the prior assistant
		// stop_reason was end_turn, we should have been idle already.
		// Otherwise we stay where we are (running, typically) — the
		// next assistant line will set the new status.
		if len(next.PendingToolUseIDs) == 0 && next.ClaudeStopReason == "end_turn" {
			next.Status = StatusIdle
		}
		return next, &Projected{Type: "user.message", Timestamp: l.Timestamp, Payload: line}

	case "system":
		switch l.Subtype {
		case "stop_hook_summary":
			// Stop-hook fired: turn complete, Claude is at the prompt.
			// Note: doesn't necessarily mean idle — if a tool_use is
			// still outstanding, Claude may resume immediately. Trust
			// the assistant's stop_reason rather than overriding here.
			next.LastEventAt = l.Timestamp
			return next, &Projected{Type: "system.stop_hook", Timestamp: l.Timestamp, Payload: line}
		case "compact_boundary":
			next.LastEventAt = l.Timestamp
			return next, &Projected{Type: "system.compact_boundary", Timestamp: l.Timestamp, Payload: line}
		case "turn_duration":
			// Informational; project as a projected event for the UI's
			// recent-events ring buffer but don't change state.
			return next, &Projected{Type: "system.turn_duration", Timestamp: l.Timestamp, Payload: line}
		default:
			// Other system subtypes are informational.
			return next, &Projected{Type: "system." + safeSubtype(l.Subtype), Timestamp: l.Timestamp, Payload: line}
		}

	case "permission-mode":
		next.PermissionMode = l.PermissionMode
		return next, &Projected{Type: "permission.mode", Timestamp: l.Timestamp, Payload: line}

	case "last-prompt":
		// Useful for the dashboard's "what's the agent working on" view;
		// project it but don't mutate status.
		return next, &Projected{Type: "last.prompt", Timestamp: l.Timestamp, Payload: line}

	// Noise — ignored entirely. Do not project.
	case "file-history-snapshot",
		"attachment",
		"worktree-state",
		"queue-operation",
		"pr-link",
		"hook":
		return next, nil

	default:
		// Unknown type. Don't mutate status; project so it's visible in
		// the recent-events ring (helps us notice new entry types
		// without silently dropping them).
		return next, &Projected{Type: "unknown." + safeSubtype(l.Type), Timestamp: l.Timestamp, Payload: line}
	}
}

// notificationLine is the shape of one line in ~/.gru/notify/<sid>.jsonl.
// The hook script appends the raw Claude Code Notification hook JSON,
// which has hook_event_name=Notification and a notification_type field.
type notificationLine struct {
	HookEventName    string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
}

func deriveNotification(next State, line []byte) (State, *Projected) {
	var n notificationLine
	if err := json.Unmarshal(line, &n); err != nil {
		return next, nil
	}
	if n.HookEventName != "Notification" {
		// Defensive: only the Notification hook should be writing this
		// file. Anything else is ignored to keep the contract narrow.
		return next, nil
	}
	switch n.NotificationType {
	case "permission_prompt", "elicitation_dialog", "idle_prompt":
		prev := next.Status
		next.Status = StatusNeedsAttention
		next.AttentionScore = 1.0
		next.LastEventAt = time.Now().UTC().Format(time.RFC3339)
		// Emit a session.transition event so the frontend can surface
		// the change without re-deriving status itself.
		payload, _ := json.Marshal(map[string]string{
			"from": string(prev),
			"to":   string(next.Status),
			"why":  "notification:" + n.NotificationType,
		})
		return next, &Projected{Type: "session.transition", Payload: payload}
	default:
		// Other notification types are informational.
		return next, &Projected{Type: "notification.info", Payload: line}
	}
}

// supervisorLine is what the supervisor injects when it detects that
// the tmux pane backing a session has gone away. The shape is small
// and we control both ends — kept as a struct rather than free-form
// JSON to avoid accidentally accepting external input.
type supervisorLine struct {
	Kind        string `json:"kind"` // "claude_pid_exit"
	WasIdle     bool   `json:"was_idle,omitempty"`
	WasAttn     bool   `json:"was_needs_attention,omitempty"`
	TmuxSession string `json:"tmux_session,omitempty"`
}

func deriveSupervisor(next State, line []byte) (State, *Projected) {
	var s supervisorLine
	if err := json.Unmarshal(line, &s); err != nil {
		return next, nil
	}
	if s.Kind != "claude_pid_exit" {
		return next, nil
	}
	prev := next.Status
	// Sessions that were idle/needs_attention "completed normally" when
	// the user closed the pane; running/starting sessions crashed.
	if s.WasIdle || s.WasAttn || prev == StatusIdle || prev == StatusNeedsAttention {
		next.Status = StatusCompleted
	} else {
		next.Status = StatusErrored
	}
	next.AttentionScore = 0
	payload, _ := json.Marshal(map[string]string{
		"from": string(prev),
		"to":   string(next.Status),
		"why":  "claude_pid_exit",
	})
	return next, &Projected{Type: "session.transition", Payload: payload}
}

// safeSubtype substitutes a placeholder for empty/non-printable
// subtypes when synthesizing event row types. Keeps the events.type
// column from carrying surprising whitespace/control characters.
func safeSubtype(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unspecified"
	}
	return s
}
