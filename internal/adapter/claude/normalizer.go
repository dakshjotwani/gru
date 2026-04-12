package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/google/uuid"
)

// hookPayload is the raw shape of a Claude Code hook event.
type hookPayload struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name,omitempty"`
	ToolInput     json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse  json.RawMessage `json:"tool_response,omitempty"`
	Message       string          `json:"message,omitempty"`
}

// Normalizer converts Claude Code hook payloads into GruEvents.
type Normalizer struct{}

// NewNormalizer returns a ready-to-use Normalizer.
func NewNormalizer() *Normalizer { return &Normalizer{} }

// RuntimeID satisfies EventNormalizer; identifies Claude Code events.
func (n *Normalizer) RuntimeID() string { return "claude-code" }

// Normalize parses raw Claude Code hook JSON and returns a GruEvent.
// SessionID is intentionally left empty — the ingestion handler sets it from
// the X-Gru-Session-ID request header after this call returns.
// Returns an error if hook_event_name is unrecognised.
func (n *Normalizer) Normalize(_ context.Context, raw json.RawMessage) (*adapter.GruEvent, error) {
	var p hookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("claude normalizer: unmarshal: %w", err)
	}

	eventType, err := mapEventType(p.HookEventName)
	if err != nil {
		return nil, err
	}

	return &adapter.GruEvent{
		ID:        uuid.NewString(),
		Runtime:   "claude-code",
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Payload:   raw,
	}, nil
}

// mapEventType converts a Claude Code hook_event_name to a GruEvent EventType.
func mapEventType(name string) (adapter.EventType, error) {
	switch name {
	case "PreToolUse":
		return adapter.EventToolPre, nil
	case "PostToolUse":
		return adapter.EventToolPost, nil
	case "PostToolUseFailure":
		return adapter.EventToolError, nil
	case "Stop":
		return adapter.EventSessionEnd, nil
	case "Notification":
		return adapter.EventNotification, nil
	case "SubagentStart":
		return adapter.EventSubagentStart, nil
	case "SubagentStop":
		return adapter.EventSubagentEnd, nil
	default:
		return "", fmt.Errorf("claude normalizer: unknown hook_event_name %q", name)
	}
}
