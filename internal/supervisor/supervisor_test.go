package supervisor_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/ingest"
	"github.com/dakshjotwani/gru/internal/supervisor"
)

// fakeSessionStore satisfies supervisor.SessionStore. It only needs to
// list live sessions; the supervisor no longer writes status.
type fakeSessionStore struct {
	sessions []supervisor.LiveSession
}

func (f *fakeSessionStore) ListLiveSessions(_ context.Context) ([]supervisor.LiveSession, error) {
	return f.sessions, nil
}

// fakeEmitter records every supervisor event so tests can assert
// what would have been delivered to the ingest log. Pass em.Sink as
// the EventSink callback to supervisor.New / NewWithRunner.
type fakeEmitter struct {
	emitted []emittedEvent
}

type emittedEvent struct {
	SessionID string
	Event     ingest.Event
}

func (f *fakeEmitter) Sink(sessionID string, ev ingest.Event) error {
	f.emitted = append(f.emitted, emittedEvent{SessionID: sessionID, Event: ev})
	return nil
}

// fakeTmuxRunner models `tmux list-panes -t session:window -F #{pane_dead}`.
// Any window listed in windowsBySession returns "0" (live pane);
// unlisted targets return an error (mimicking tmux "can't find window").
type fakeTmuxRunner struct {
	windowsBySession map[string][]string
}

func (f *fakeTmuxRunner) Output(args ...string) ([]byte, error) {
	if len(args) >= 3 && args[0] == "list-panes" {
		target := args[2]
		// Single-session-per-window v2: target may be "session" alone.
		// Look up by full target as a key first.
		if windows, ok := f.windowsBySession[target]; ok {
			if len(windows) == 0 {
				return nil, fmt.Errorf("can't find window: %s", target)
			}
			return []byte("0\n"), nil
		}
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
		return nil, fmt.Errorf("can't find target: %s", target)
	}
	return nil, nil
}

// deadPaneTmuxRunner mimics tmux holding a window open with all panes
// dead (remain-on-exit=on).
type deadPaneTmuxRunner struct{}

func (deadPaneTmuxRunner) Output(args ...string) ([]byte, error) {
	if len(args) >= 3 && args[0] == "list-panes" {
		return []byte("1\n"), nil
	}
	return nil, nil
}

// ── new contract: pid_exit emitted, status NOT written ──────────────

func TestSupervisor_emitsPidExitForRunningWithGoneWindow(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-av-sim": {}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-dead", TmuxSession: "gru-av-sim", TmuxWindow: "feat·11111111", Status: "running",
	}}}
	em := &fakeEmitter{}
	sv := supervisor.NewWithRunner(store, em.Sink, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())

	if len(em.emitted) != 1 {
		t.Fatalf("expected 1 emitted event, got %d", len(em.emitted))
	}
	if em.emitted[0].SessionID != "sess-dead" {
		t.Fatalf("emitted for wrong session: %s", em.emitted[0].SessionID)
	}
	ev := em.emitted[0].Event
	if ev.Type != ingest.TypeProcessExited {
		t.Fatalf("emitted type = %s, want process_exited", ev.Type)
	}
	if ev.Graceful == nil || *ev.Graceful != false {
		t.Fatalf("running session should yield graceful=false; got %v", ev.Graceful)
	}
}

func TestSupervisor_emitsWasIdleForGoneIdleSession(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-av-sim": {}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-idle", TmuxSession: "gru-av-sim", TmuxWindow: "w1", Status: "idle",
	}}}
	em := &fakeEmitter{}
	sv := supervisor.NewWithRunner(store, em.Sink, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())

	if len(em.emitted) != 1 {
		t.Fatalf("expected 1 emitted event, got %d", len(em.emitted))
	}
	ev := em.emitted[0].Event
	if ev.Graceful == nil || *ev.Graceful != true {
		t.Fatalf("idle session pid_exit should yield graceful=true; got %v", ev.Graceful)
	}
}

func TestSupervisor_deadPaneCountsAsGone(t *testing.T) {
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-dead-pane", TmuxSession: "gru-foo", TmuxWindow: "w1", Status: "running",
	}}}
	em := &fakeEmitter{}
	sv := supervisor.NewWithRunner(store, em.Sink, 50*time.Millisecond, deadPaneTmuxRunner{})
	sv.ReconcileOnce(context.Background())
	if len(em.emitted) != 1 {
		t.Fatalf("expected dead-pane session to emit pid_exit, got %d events", len(em.emitted))
	}
}

func TestSupervisor_aliveWindowEmitsNothing(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-av-sim": {"w1"}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-alive", TmuxSession: "gru-av-sim", TmuxWindow: "w1", Status: "running",
	}}}
	em := &fakeEmitter{}
	sv := supervisor.NewWithRunner(store, em.Sink, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())
	if len(em.emitted) != 0 {
		t.Fatalf("alive window should emit nothing; got %v", em.emitted)
	}
}

func TestSupervisor_doesNotDoubleEmit(t *testing.T) {
	// On a single supervisor instance, a session whose pane is already
	// gone should emit pid_exit exactly once even if the reconcile
	// loop runs many times before the tailer reads the file.
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-av-sim": {}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-once", TmuxSession: "gru-av-sim", TmuxWindow: "w1", Status: "running",
	}}}
	em := &fakeEmitter{}
	sv := supervisor.NewWithRunner(store, em.Sink, 50*time.Millisecond, tmux)
	sv.ReconcileOnce(context.Background())
	sv.ReconcileOnce(context.Background())
	sv.ReconcileOnce(context.Background())
	if len(em.emitted) != 1 {
		t.Fatalf("expected exactly 1 emit, got %d", len(em.emitted))
	}
}

func TestSupervisor_RunPollsRepeatedly(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-myproject": {}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "sess-poll", TmuxSession: "gru-myproject", TmuxWindow: "w1", Status: "starting",
	}}}
	em := &fakeEmitter{}
	sv := supervisor.NewWithRunner(store, em.Sink, 50*time.Millisecond, tmux)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sv.Run(ctx)
	if len(em.emitted) == 0 {
		t.Fatal("expected at least one supervisor event after Run")
	}
}
