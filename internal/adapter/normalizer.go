package adapter

import (
	"context"
	"encoding/json"
	"time"
)

// EventType is the normalized event type string.
type EventType string

const (
	// Session lifecycle.
	EventSessionStart EventType = "session.start"  // Claude Code started a session
	EventSessionIdle  EventType = "session.idle"   // Turn complete; Claude waiting for next input (Stop hook)
	EventSessionEnd   EventType = "session.end"    // Session terminated (process exited)
	EventSessionCrash EventType = "session.crash"  // Fatal error ended the session (StopFailure hook)

	// User turn.
	EventUserPrompt EventType = "user.prompt" // UserPromptSubmit — user submitted a prompt

	// Tool execution.
	EventToolPre   EventType = "tool.pre"   // PreToolUse
	EventToolPost  EventType = "tool.post"  // PostToolUse
	EventToolError EventType = "tool.error" // PostToolUseFailure

	// Notifications.
	EventNotification    EventType = "notification"             // Informational (Notification hook, generic types)
	EventNeedsAttention  EventType = "notification.needs_attention" // Claude blocked on permission or MCP elicitation

	// Subagents.
	EventSubagentStart EventType = "subagent.start"
	EventSubagentEnd   EventType = "subagent.end"
)

// GruEvent is the normalized event schema written to the store
// and broadcast to SubscribeEvents streams.
type GruEvent struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	ProjectID string          `json:"project_id"`
	Runtime   string          `json:"runtime"`
	Type      EventType       `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"` // original runtime-specific JSON
}

// EventNormalizer translates runtime-specific hook payloads into GruEvent.
// Implementations are stateless and registered at startup.
type EventNormalizer interface {
	// RuntimeID returns the runtime identifier this normalizer handles.
	// Example: "claude-code"
	RuntimeID() string

	// Normalize converts the raw hook payload into a GruEvent.
	// The returned event's ID, SessionID, ProjectID, and Timestamp must be set.
	Normalize(ctx context.Context, raw json.RawMessage) (*GruEvent, error)
}

// Registry holds registered normalizers, keyed by runtime ID.
type Registry struct {
	normalizers map[string]EventNormalizer
}

func NewRegistry() *Registry {
	return &Registry{normalizers: make(map[string]EventNormalizer)}
}

// Register adds a normalizer. Panics on duplicate runtime IDs (programming error).
func (r *Registry) Register(n EventNormalizer) {
	id := n.RuntimeID()
	if _, exists := r.normalizers[id]; exists {
		panic("adapter: duplicate normalizer for runtime: " + id)
	}
	r.normalizers[id] = n
}

// Get returns the normalizer for the given runtime ID, or nil if not found.
func (r *Registry) Get(runtimeID string) EventNormalizer {
	return r.normalizers[runtimeID]
}
