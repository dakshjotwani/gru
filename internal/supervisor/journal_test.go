package supervisor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/supervisor"
)

type fakeRespawner struct {
	calls int
	err   error
}

func (f *fakeRespawner) RespawnJournal(ctx context.Context) error {
	f.calls++
	return f.err
}

func TestSupervisor_RespawnsDeadJournal(t *testing.T) {
	// No live sessions at all — journal is missing and should be respawned.
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{}}
	store := &fakeSessionStore{sessions: nil}
	pub := &fakePublisher{}
	r := &fakeRespawner{}

	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.SetJournalRespawner(r)

	sv.ReconcileOnce(context.Background())
	if r.calls != 1 {
		t.Fatalf("expected 1 respawn call, got %d", r.calls)
	}
}

func TestSupervisor_DoesNotRespawnWhenJournalAlive(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{
		"gru-journal": {"journal·abcd1234"},
	}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "journal-1", TmuxSession: "gru-journal", TmuxWindow: "journal·abcd1234",
		Status: "running", Role: "assistant",
	}}}
	pub := &fakePublisher{}
	r := &fakeRespawner{}

	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.SetJournalRespawner(r)

	sv.ReconcileOnce(context.Background())
	if r.calls != 0 {
		t.Fatalf("expected no respawn call when journal alive, got %d", r.calls)
	}
}

func TestSupervisor_BackoffOnRespawnFailure(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{}}
	store := &fakeSessionStore{sessions: nil}
	pub := &fakePublisher{}
	r := &fakeRespawner{err: errors.New("boom")}

	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.SetJournalRespawner(r)

	// First reconcile: respawn fails, sets backoff ≥ 5s.
	sv.ReconcileOnce(context.Background())
	if r.calls != 1 {
		t.Fatalf("first reconcile: expected 1 call, got %d", r.calls)
	}
	// Immediate second reconcile: backoff should suppress the retry.
	sv.ReconcileOnce(context.Background())
	if r.calls != 1 {
		t.Fatalf("second reconcile within backoff: expected still 1 call, got %d", r.calls)
	}
}

func TestSupervisor_RespawnDisabledWhenNoRespawner(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{}}
	store := &fakeSessionStore{sessions: nil}
	pub := &fakePublisher{}

	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	// No SetJournalRespawner — should be a no-op.
	sv.ReconcileOnce(context.Background())
	// Nothing to assert on the store; the test's value is that it doesn't panic.
}

func TestSupervisor_DeadJournalRowStillMarkedErrored(t *testing.T) {
	// A journal-role session whose tmux window is gone should still be marked
	// errored/completed like any other — the respawn produces a *new* session row.
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-journal": {}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "journal-dead", TmuxSession: "gru-journal", TmuxWindow: "journal·zzz",
		Status: "running", Role: "assistant",
	}}}
	pub := &fakePublisher{}
	r := &fakeRespawner{}

	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)
	sv.SetJournalRespawner(r)

	sv.ReconcileOnce(context.Background())
	if len(store.updated) != 1 || store.updated[0].Status != "errored" {
		t.Fatalf("expected dead journal row marked errored, got %v", store.updated)
	}
	if r.calls != 1 {
		t.Fatalf("expected respawn call after journal row dies, got %d", r.calls)
	}
}
