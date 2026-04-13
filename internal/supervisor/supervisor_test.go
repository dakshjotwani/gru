package supervisor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/supervisor"
)

type fakeSessionStore struct {
	sessions []supervisor.LiveSession
	updated  []supervisor.StatusUpdate
}

func (f *fakeSessionStore) ListLiveSessions(ctx context.Context) ([]supervisor.LiveSession, error) {
	return f.sessions, nil
}

func (f *fakeSessionStore) UpdateSessionStatus(ctx context.Context, sessionID, status string) error {
	f.updated = append(f.updated, supervisor.StatusUpdate{SessionID: sessionID, Status: status})
	return nil
}

type fakePublisher struct {
	events []supervisor.ExitEvent
}

func (f *fakePublisher) PublishExit(ctx context.Context, e supervisor.ExitEvent) {
	f.events = append(f.events, e)
}

type fakeTmuxRunner struct {
	windowsBySession map[string][]string
}

func (f *fakeTmuxRunner) Output(args ...string) ([]byte, error) {
	if len(args) >= 3 && args[0] == "list-windows" {
		session := args[2]
		windows := f.windowsBySession[session]
		return []byte(strings.Join(windows, "\n") + "\n"), nil
	}
	return nil, nil
}

func TestSupervisor_MarksDeadRunningSessionErrored(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-av-sim": {}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-dead", TmuxSession: "gru-av-sim", TmuxWindow: "feat-dev·abcd1234", Status: "running",
	}}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 1 {
		t.Fatalf("expected 1 status update, got %d", len(store.updated))
	}
	if store.updated[0].SessionID != "sess-dead" {
		t.Errorf("updated session ID = %q, want %q", store.updated[0].SessionID, "sess-dead")
	}
	if store.updated[0].Status != "errored" {
		t.Errorf("updated status = %q, want %q", store.updated[0].Status, "errored")
	}
	if len(pub.events) != 1 || pub.events[0].SessionID != "sess-dead" {
		t.Errorf("expected 1 exit event for sess-dead, got %v", pub.events)
	}
	if pub.events[0].NewStatus != "errored" {
		t.Errorf("exit event status = %q, want %q", pub.events[0].NewStatus, "errored")
	}
}

func TestSupervisor_MarksDeadIdleSessionCompleted(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-av-sim": {}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-idle", TmuxSession: "gru-av-sim", TmuxWindow: "feat-dev·abcd1234", Status: "idle",
	}}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 1 {
		t.Fatalf("expected 1 status update, got %d", len(store.updated))
	}
	if store.updated[0].Status != "completed" {
		t.Errorf("updated status = %q, want %q", store.updated[0].Status, "completed")
	}
	if pub.events[0].NewStatus != "completed" {
		t.Errorf("exit event status = %q, want %q", pub.events[0].NewStatus, "completed")
	}
}

func TestSupervisor_DoesNotMarkAliveWindow(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-av-sim": {"feat-dev·abcd1234"}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-alive", TmuxSession: "gru-av-sim", TmuxWindow: "feat-dev·abcd1234", Status: "running",
	}}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 0 {
		t.Errorf("expected no updates for alive window, got %v", store.updated)
	}
	if len(pub.events) != 0 {
		t.Errorf("expected no exit events, got %v", pub.events)
	}
}

func TestSupervisor_RunPollsRepeatedly(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-myproject": {}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-poll", TmuxSession: "gru-myproject", TmuxWindow: "feat-dev·poll1234", Status: "starting",
	}}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sv.Run(ctx)
	if len(store.updated) == 0 {
		t.Fatal("expected at least one status update after supervisor run")
	}
}
