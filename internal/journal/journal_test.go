package journal_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dakshjotwani/gru/internal/config"
	"github.com/dakshjotwani/gru/internal/controller"
	"github.com/dakshjotwani/gru/internal/journal"
	"github.com/dakshjotwani/gru/internal/store"
)

// fakeController records the most recent LaunchOptions and returns canned handles.
type fakeController struct {
	lastOpts controller.LaunchOptions
	calls    int
}

func (f *fakeController) RuntimeID() string                        { return "claude-code" }
func (f *fakeController) Capabilities() []controller.Capability    { return nil }
func (f *fakeController) Launch(_ context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
	f.lastOpts = opts
	f.calls++
	return &controller.SessionHandle{
		SessionID:   opts.SessionID,
		TmuxSession: "gru-journal",
		TmuxWindow:  "journal·" + opts.SessionID[:8],
	}, nil
}

func newTestCfg(t *testing.T) *config.Config {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	enabled := true
	return &config.Config{
		Journal: config.JournalConfig{
			Enabled:        &enabled,
			WorkspaceRoots: []string{filepath.Join(home, "workspace")},
		},
	}
}

func TestEnsure_SpawnsWhenNoJournalExists(t *testing.T) {
	cfg := newTestCfg(t)
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fc := &fakeController{}
	reg := controller.NewRegistry()
	reg.Register(fc)

	if err := journal.Ensure(context.Background(), s, reg, cfg); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if fc.calls != 1 {
		t.Fatalf("expected 1 controller launch, got %d", fc.calls)
	}

	// The created session row should have role=assistant and status=starting.
	row, err := s.Queries().GetAssistantSession(context.Background())
	if err != nil {
		t.Fatalf("GetAssistantSession: %v", err)
	}
	if row.Role != "assistant" {
		t.Errorf("role = %q, want %q", row.Role, "assistant")
	}
	if row.Status != "starting" {
		t.Errorf("status = %q, want starting", row.Status)
	}

	// The journal dir should exist.
	home := os.Getenv("HOME")
	info, err := os.Stat(filepath.Join(home, ".gru", "journal"))
	if err != nil {
		t.Fatalf("journal dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected journal path to be a directory")
	}

	// Workspace roots env var should be piped through to the controller.
	got := fc.lastOpts.Env["GRU_JOURNAL_WORKSPACE_ROOTS"]
	if got == "" {
		t.Errorf("expected GRU_JOURNAL_WORKSPACE_ROOTS env var to be set")
	}
	if fc.lastOpts.ExtraPrompt == "" {
		t.Errorf("expected journal system prompt to be passed as ExtraPrompt")
	}
}

func TestEnsure_NoOpWhenLiveJournalExists(t *testing.T) {
	cfg := newTestCfg(t)
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fc := &fakeController{}
	reg := controller.NewRegistry()
	reg.Register(fc)

	// First call spawns.
	if err := journal.Ensure(context.Background(), s, reg, cfg); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	// Second call should be a no-op because a live journal session already exists.
	if err := journal.Ensure(context.Background(), s, reg, cfg); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if fc.calls != 1 {
		t.Fatalf("expected no additional launch on second Ensure, got %d total calls", fc.calls)
	}
}

func TestEnsure_DisabledIsNoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	disabled := false
	cfg := &config.Config{
		Journal: config.JournalConfig{Enabled: &disabled},
	}
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fc := &fakeController{}
	reg := controller.NewRegistry()
	reg.Register(fc)

	if err := journal.Ensure(context.Background(), s, reg, cfg); err != nil {
		t.Fatalf("Ensure with disabled: %v", err)
	}
	if fc.calls != 0 {
		t.Errorf("expected no launch when journal disabled, got %d", fc.calls)
	}
}

func TestSpawn_AlwaysCreatesNewSession(t *testing.T) {
	cfg := newTestCfg(t)
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	fc := &fakeController{}
	reg := controller.NewRegistry()
	reg.Register(fc)

	id1, err := journal.Spawn(context.Background(), s, reg, cfg)
	if err != nil {
		t.Fatalf("first Spawn: %v", err)
	}
	id2, err := journal.Spawn(context.Background(), s, reg, cfg)
	if err != nil {
		t.Fatalf("second Spawn: %v", err)
	}
	if id1 == id2 {
		t.Errorf("Spawn returned same id twice: %s", id1)
	}
	if fc.calls != 2 {
		t.Errorf("expected 2 launches from two Spawn calls, got %d", fc.calls)
	}
}
