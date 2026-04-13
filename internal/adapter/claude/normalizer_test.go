package claude_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/adapter/claude"
)

func TestClaudeNormalizer_RuntimeID(t *testing.T) {
	n := claude.NewNormalizer()
	if got := n.RuntimeID(); got != "claude-code" {
		t.Fatalf("RuntimeID() = %q; want %q", got, "claude-code")
	}
}

func TestClaudeNormalizer_PreToolUse(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{
		"hook_event_name": "PreToolUse",
		"session_id": "sess-abc",
		"cwd": "/home/user/myproject",
		"tool_name": "Bash",
		"tool_input": {"command": "ls"}
	}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventToolPre {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventToolPre)
	}
	if ev.ID == "" {
		t.Error("ID must not be empty")
	}
	if ev.Runtime != "claude-code" {
		t.Errorf("Runtime = %q; want %q", ev.Runtime, "claude-code")
	}
	if ev.Timestamp.IsZero() {
		t.Error("Timestamp must not be zero")
	}
}

func TestClaudeNormalizer_PostToolUse(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"PostToolUse","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventToolPost {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventToolPost)
	}
}

func TestClaudeNormalizer_PostToolUseFailure(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"PostToolUseFailure","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventToolError {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventToolError)
	}
}

func TestClaudeNormalizer_Stop(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"Stop","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Stop means the turn is complete and Claude is waiting for input — idle, not ended.
	if ev.Type != adapter.EventSessionIdle {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventSessionIdle)
	}
}

func TestClaudeNormalizer_SessionStart(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"SessionStart","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventSessionStart {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventSessionStart)
	}
}

func TestClaudeNormalizer_StopFailure(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"StopFailure","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventSessionCrash {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventSessionCrash)
	}
}

func TestClaudeNormalizer_NotificationPermissionPrompt(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"Notification","session_id":"s1","cwd":"/p","notification_type":"permission_prompt"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventNeedsAttention {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventNeedsAttention)
	}
}

func TestClaudeNormalizer_NotificationElicitation(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"Notification","session_id":"s1","cwd":"/p","notification_type":"elicitation_dialog"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventNeedsAttention {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventNeedsAttention)
	}
}

func TestClaudeNormalizer_NotificationGeneric(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"Notification","session_id":"s1","cwd":"/p","notification_type":"auth_success"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventNotification {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventNotification)
	}
}

func TestClaudeNormalizer_Notification(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"Notification","session_id":"s1","cwd":"/p","message":"hello"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventNotification {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventNotification)
	}
}

func TestClaudeNormalizer_SubagentStart(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"SubagentStart","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventSubagentStart {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventSubagentStart)
	}
}

func TestClaudeNormalizer_SubagentStop(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"SubagentStop","session_id":"s1","cwd":"/p"}`)
	ev, err := n.Normalize(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != adapter.EventSubagentEnd {
		t.Errorf("Type = %q; want %q", ev.Type, adapter.EventSubagentEnd)
	}
}

func TestClaudeNormalizer_UnknownEvent(t *testing.T) {
	n := claude.NewNormalizer()
	raw := json.RawMessage(`{"hook_event_name":"WhateverNew","session_id":"s1","cwd":"/p"}`)
	_, err := n.Normalize(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for unknown hook_event_name, got nil")
	}
}

func TestClaudeNormalizer_PayloadPreserved(t *testing.T) {
	n := claude.NewNormalizer()
	rawStr := `{"hook_event_name":"PreToolUse","session_id":"s1","cwd":"/p","tool_name":"Bash","tool_input":{"command":"ls"}}`
	ev, err := n.Normalize(context.Background(), json.RawMessage(rawStr))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ev.Payload) == 0 {
		t.Error("Payload must not be empty")
	}
}
