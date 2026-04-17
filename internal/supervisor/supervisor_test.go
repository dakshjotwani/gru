package supervisor_test

import (
	"context"
	"fmt"
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
	events        []supervisor.ExitEvent
	statusChanges []supervisor.StatusChangeEvent
}

func (f *fakePublisher) PublishExit(ctx context.Context, e supervisor.ExitEvent) {
	f.events = append(f.events, e)
}

func (f *fakePublisher) PublishStatusChange(ctx context.Context, e supervisor.StatusChangeEvent) {
	f.statusChanges = append(f.statusChanges, e)
}

// fakeTmuxRunner models `tmux list-panes -t session:window -F #{pane_dead}`.
// The test setup provides the session→window list (matching production naming);
// any listed window is treated as having one live pane ("0"), and unlisted
// targets return an error (mimicking tmux reporting "can't find window").
type fakeTmuxRunner struct {
	windowsBySession map[string][]string
}

func (f *fakeTmuxRunner) Output(args ...string) ([]byte, error) {
	if len(args) >= 3 && args[0] == "list-panes" {
		target := args[2] // "session:window"
		for i := 0; i < len(target); i++ {
			if target[i] != ':' {
				continue
			}
			sess, win := target[:i], target[i+1:]
			for _, w := range f.windowsBySession[sess] {
				if w == win {
					return []byte("0\n"), nil
				}
			}
			return nil, fmt.Errorf("can't find window: %s", target)
		}
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

// deadPaneTmuxRunner models tmux keeping a window listed but with all panes
// dead (remain-on-exit=on). list-panes succeeds but returns "1" for every
// pane, which must NOT be treated as alive.
type deadPaneTmuxRunner struct{}

func (deadPaneTmuxRunner) Output(args ...string) ([]byte, error) {
	if len(args) >= 3 && args[0] == "list-panes" {
		return []byte("1\n"), nil
	}
	return nil, nil
}

func TestSupervisor_MarksDeadPaneErrored(t *testing.T) {
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-dead-pane", TmuxSession: "gru-foo", TmuxWindow: "feat·11111111", Status: "running",
	}}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, deadPaneTmuxRunner{})
	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 1 || store.updated[0].Status != "errored" {
		t.Fatalf("expected dead-pane session marked errored, got %v", store.updated)
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

// Staleness heuristic tests: the supervisor should flip long-silent `running`
// sessions to `needs_attention` as a safety net for dropped hooks, but must
// never interrupt legitimate long-running tool calls.

func staleTime(ago time.Duration) *time.Time {
	t := time.Now().Add(-ago)
	return &t
}

func TestSupervisor_FlipsStaleRunningToNeedsAttention(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-p": {"feat·11111111"}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID:            "sess-stale",
		TmuxSession:   "gru-p",
		TmuxWindow:    "feat·11111111",
		Status:        "running",
		LastEventAt:   staleTime(30 * time.Minute),
		LastEventType: "tool.post", // not inside a tool call
	}}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 1 || store.updated[0].Status != "needs_attention" {
		t.Fatalf("expected needs_attention flip, got %v", store.updated)
	}
	if len(pub.statusChanges) != 1 || pub.statusChanges[0].NewStatus != "needs_attention" {
		t.Errorf("expected status-change event, got %v", pub.statusChanges)
	}
}

func TestSupervisor_LeavesToolCallAlone(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-p": {"feat·11111111"}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID:            "sess-longtool",
		TmuxSession:   "gru-p",
		TmuxWindow:    "feat·11111111",
		Status:        "running",
		LastEventAt:   staleTime(45 * time.Minute), // way past threshold
		LastEventType: "tool.pre",                  // inside a tool call
	}}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 0 {
		t.Errorf("expected no updates for tool.pre in-flight, got %v", store.updated)
	}
	if len(pub.statusChanges) != 0 {
		t.Errorf("expected no status-change events, got %v", pub.statusChanges)
	}
}

func TestSupervisor_LeavesSubagentRunAlone(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-p": {"feat·11111111"}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID:            "sess-subagent",
		TmuxSession:   "gru-p",
		TmuxWindow:    "feat·11111111",
		Status:        "running",
		LastEventAt:   staleTime(30 * time.Minute),
		LastEventType: "subagent.start",
	}}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 0 {
		t.Errorf("expected no updates for in-flight subagent, got %v", store.updated)
	}
}

func TestSupervisor_LeavesFreshRunningAlone(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-p": {"feat·11111111"}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID:            "sess-fresh",
		TmuxSession:   "gru-p",
		TmuxWindow:    "feat·11111111",
		Status:        "running",
		LastEventAt:   staleTime(2 * time.Minute), // well under threshold
		LastEventType: "tool.post",
	}}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 0 {
		t.Errorf("expected no updates for fresh session, got %v", store.updated)
	}
}

func TestSupervisor_SkipsNonRunningStatuses(t *testing.T) {
	// idle and needs_attention sessions with stale last_event_at must not be
	// flipped — idle means the turn is genuinely done, and needs_attention
	// is already the target state.
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-p": {"w1", "w2"}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{
		{ID: "sess-idle", TmuxSession: "gru-p", TmuxWindow: "w1", Status: "idle",
			LastEventAt: staleTime(1 * time.Hour), LastEventType: "session.idle"},
		{ID: "sess-attn", TmuxSession: "gru-p", TmuxWindow: "w2", Status: "needs_attention",
			LastEventAt: staleTime(1 * time.Hour), LastEventType: "notification.needs_attention"},
	}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 0 {
		t.Errorf("expected no updates, got %v", store.updated)
	}
}

func TestSupervisor_DisabledThresholdSkipsHeuristic(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-p": {"w1"}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-stale", TmuxSession: "gru-p", TmuxWindow: "w1", Status: "running",
		LastEventAt: staleTime(2 * time.Hour), LastEventType: "tool.post",
	}}}
	pub := &fakePublisher{}
	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.SetIdleThreshold(0) // disabled
	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 0 {
		t.Errorf("expected heuristic disabled, got updates %v", store.updated)
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
