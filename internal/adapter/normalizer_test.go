package adapter_test

import (
	"testing"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/adapter/claude"
)

func TestRegistry_ClaudeCodeRegistered(t *testing.T) {
	r := adapter.NewRegistry()
	r.Register(claude.NewNormalizer())
	n := r.Get("claude-code")
	if n == nil {
		t.Fatal("expected claude-code normalizer to be registered, got nil")
	}
	if n.RuntimeID() != "claude-code" {
		t.Errorf("RuntimeID() = %q; want %q", n.RuntimeID(), "claude-code")
	}
}
