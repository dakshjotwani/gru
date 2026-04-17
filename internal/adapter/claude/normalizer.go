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
	HookEventName    string          `json:"hook_event_name"`
	ToolName         string          `json:"tool_name,omitempty"`
	ToolInput        json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse     json.RawMessage `json:"tool_response,omitempty"`
	Message          string          `json:"message,omitempty"`
	NotificationType string          `json:"notification_type,omitempty"` // Notification hook
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

	eventType, err := mapEventType(p)
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

// mapEventType converts a Claude Code hook payload to a GruEvent EventType.
//
// Hook → event type → intended session status:
//
//	SessionStart        → session.start              → running
//	PreToolUse          → tool.pre                   → running
//	PostToolUse         → tool.post                  (stay running)
//	PostToolUseFailure  → tool.error                 (stay running)
//	SubagentStart       → subagent.start             (stay running)
//	SubagentStop        → subagent.end               (stay running)
//	Stop                → session.idle               → idle   (turn done, waiting for input)
//	StopFailure         → session.crash              → errored
//	Notification (permission_prompt, elicitation_dialog, idle_prompt) → notification.needs_attention → needs_attention
//	Notification (other) → notification              (informational)
func mapEventType(p hookPayload) (adapter.EventType, error) {
	switch p.HookEventName {
	case "SessionStart":
		return adapter.EventSessionStart, nil
	case "PreToolUse":
		return adapter.EventToolPre, nil
	case "PostToolUse":
		return adapter.EventToolPost, nil
	case "PostToolUseFailure":
		return adapter.EventToolError, nil
	case "Stop":
		// Stop fires when a turn completes and Claude is back at the prompt
		// waiting for the next instruction — this is idle, not session end.
		return adapter.EventSessionIdle, nil
	case "StopFailure":
		// StopFailure fires when a turn ends due to an API error (rate limit,
		// auth failure, server error, etc.) — treat as a crash.
		return adapter.EventSessionCrash, nil
	case "Notification":
		// Discriminate by notification_type: permission requests, MCP
		// elicitations, and Claude's "idle waiting for input" prompt all
		// block the session and require user action. idle_prompt fires
		// ~60s after the Stop hook and is our reliable signal that the
		// agent is stuck at the prompt (and the Stop hook may itself have
		// failed to deliver).
		switch p.NotificationType {
		case "permission_prompt", "elicitation_dialog", "idle_prompt":
			return adapter.EventNeedsAttention, nil
		default:
			return adapter.EventNotification, nil
		}
	case "SubagentStart":
		return adapter.EventSubagentStart, nil
	case "SubagentStop":
		return adapter.EventSubagentEnd, nil
	default:
		return "", fmt.Errorf("claude normalizer: unknown hook_event_name %q", p.HookEventName)
	}
}
