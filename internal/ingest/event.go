// Package ingest is gru's anti-corruption layer between Claude Code's
// hook surface (and gru's own internal status-affecting events) and
// the per-session event log the tailer reads. Every line in
// ~/.gru/events/<sid>.jsonl goes through this package — no direct
// writes from controllers, hooks, or handlers — so the tailer's input
// shape is gru's, not Anthropic's.
//
// See docs/adr/0002-rev3-hook-driven-event-log.md for the design
// rationale.
package ingest

import "encoding/json"

// SchemaVersion is the per-line `v` field. Bump when introducing a
// breaking change to the Event grammar. The tailer skips (with a log
// line) any event whose Version exceeds what it knows.
const SchemaVersion = 1

// Type is the closed set of gru-internal event types. Translation
// from Claude hooks (or supervisor / CLI sources) lands on one of
// these; everything else is TypeUnknown.
type Type string

const (
	TypeSessionStarted      Type = "session_started"
	TypeTurnStarted         Type = "turn_started"
	TypeTurnEnded           Type = "turn_ended"
	TypeToolCompleted       Type = "tool_completed"
	TypeAttentionRequested  Type = "attention_requested"
	TypeProcessExited       Type = "process_exited"
	TypeKilledByUser        Type = "killed_by_user"
	TypeUnknown             Type = "unknown"
)

// Event is one append-only line in the per-session log. The tailer
// folds these through state.Derive in order. Sparse fields are
// populated only by the events that need them — TranslateClaudeHook
// and the supervisor/CLI emitters fill what's relevant and leave the
// rest zero.
type Event struct {
	Version int    `json:"v"`
	Type    Type   `json:"type"`
	Ts      string `json:"ts"` // RFC3339, UTC

	// turn_started: trigger explains what woke the agent.
	// One of: "user_prompt", "tool_result", "session_resume".
	Trigger string `json:"trigger,omitempty"`

	// turn_ended: stop_reason mirrors Claude's assistant.stop_reason
	// (end_turn / tool_use / stop_sequence / max_tokens / refusal /
	// error).
	StopReason string `json:"stop_reason,omitempty"`

	// tool_completed: which tool, and whether it succeeded.
	Tool string `json:"tool,omitempty"`
	Ok   *bool  `json:"ok,omitempty"`

	// attention_requested: which Notification subtype fired.
	// One of: "idle_prompt", "permission_prompt", "elicitation_dialog".
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`

	// process_exited: whether the pane went away while the agent was
	// at a calm state (idle/needs_attention) vs in-flight.
	Graceful    *bool  `json:"graceful,omitempty"`
	PriorStatus string `json:"prior_status,omitempty"`

	// unknown: opaque pass-through for hook events we register but
	// don't currently translate. Lets us add fold-arms without
	// changing the translator.
	ClaudeEvent string          `json:"claude_event,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// boolPtr is a tiny helper for the optional bool fields. The Event
// shape uses *bool to distinguish "unset" from "false" on the wire.
func boolPtr(b bool) *bool { return &b }
