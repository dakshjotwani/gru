package server

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
)

// mockSuggester implements nameSuggester for testing.
type mockSuggester struct {
	name        string
	description string
	err         error
	calls       []suggestCall
}

type suggestCall struct {
	prompt     string
	projectDir string
}

func (m *mockSuggester) suggest(ctx context.Context, prompt, projectDir string) (string, string, error) {
	m.calls = append(m.calls, suggestCall{prompt: prompt, projectDir: projectDir})
	return m.name, m.description, m.err
}

func newSuggestTestService(t *testing.T) *Service {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	pub := ingestion.NewPublisher()
	return NewService(s, pub)
}

func TestSuggestSessionName_disabled(t *testing.T) {
	svc := newSuggestTestService(t)
	svc.setSuggester(nil) // explicitly disable to test the nil guard

	resp, err := svc.SuggestSessionName(context.Background(), connect.NewRequest(&gruv1.SuggestSessionNameRequest{
		Prompt:     "fix the auth token expiry bug",
		ProjectDir: "/home/user/myproject",
	}))
	if err != nil {
		t.Fatalf("SuggestSessionName: unexpected error: %v", err)
	}
	if resp.Msg.Name != "" || resp.Msg.Description != "" {
		t.Errorf("expected empty response when disabled, got name=%q desc=%q", resp.Msg.Name, resp.Msg.Description)
	}
}

func TestSuggestSessionName_success(t *testing.T) {
	svc := newSuggestTestService(t)

	mock := &mockSuggester{
		name:        "auth-token-expiry-fix",
		description: "Fixes the bug where auth tokens expire too soon.",
	}
	svc.setSuggester(mock)

	resp, err := svc.SuggestSessionName(context.Background(), connect.NewRequest(&gruv1.SuggestSessionNameRequest{
		Prompt:     "fix the auth token expiry bug",
		ProjectDir: "/home/user/myproject",
	}))
	if err != nil {
		t.Fatalf("SuggestSessionName: unexpected error: %v", err)
	}
	if resp.Msg.Name != "auth-token-expiry-fix" {
		t.Errorf("Name = %q, want %q", resp.Msg.Name, "auth-token-expiry-fix")
	}
	if resp.Msg.Description != "Fixes the bug where auth tokens expire too soon." {
		t.Errorf("Description = %q", resp.Msg.Description)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("suggest called %d times, want 1", len(mock.calls))
	}
	if mock.calls[0].prompt != "fix the auth token expiry bug" {
		t.Errorf("prompt passed to suggester = %q", mock.calls[0].prompt)
	}
	if mock.calls[0].projectDir != "/home/user/myproject" {
		t.Errorf("projectDir passed to suggester = %q", mock.calls[0].projectDir)
	}
}

func TestSuggestSessionName_error_returns_empty(t *testing.T) {
	svc := newSuggestTestService(t)

	mock := &mockSuggester{err: errors.New("anthropic API unreachable")}
	svc.setSuggester(mock)

	// Errors from the suggester must not propagate — return empty strings instead.
	resp, err := svc.SuggestSessionName(context.Background(), connect.NewRequest(&gruv1.SuggestSessionNameRequest{
		Prompt: "do something",
	}))
	if err != nil {
		t.Fatalf("SuggestSessionName: RPC must not error when suggester fails, got: %v", err)
	}
	if resp.Msg.Name != "" || resp.Msg.Description != "" {
		t.Errorf("expected empty response on suggester error, got name=%q desc=%q", resp.Msg.Name, resp.Msg.Description)
	}
}
