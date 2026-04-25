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

func (f *fakeRespawner) RespawnJournal(_ context.Context) error {
	f.calls++
	return f.err
}

func TestSupervisor_RespawnsDeadJournal(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{}}
	store := &fakeSessionStore{sessions: nil}
	em := &fakeEmitter{}
	r := &fakeRespawner{}

	sv := supervisor.NewWithRunner(store, em, 50*time.Millisecond, tmux)
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
	em := &fakeEmitter{}
	r := &fakeRespawner{}

	sv := supervisor.NewWithRunner(store, em, 50*time.Millisecond, tmux)
	sv.SetJournalRespawner(r)

	sv.ReconcileOnce(context.Background())
	if r.calls != 0 {
		t.Fatalf("expected no respawn call when journal alive, got %d", r.calls)
	}
}

func TestSupervisor_BackoffOnRespawnFailure(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{}}
	store := &fakeSessionStore{sessions: nil}
	em := &fakeEmitter{}
	r := &fakeRespawner{err: errors.New("boom")}

	sv := supervisor.NewWithRunner(store, em, 50*time.Millisecond, tmux)
	sv.SetJournalRespawner(r)

	sv.ReconcileOnce(context.Background())
	if r.calls != 1 {
		t.Fatalf("first reconcile: expected 1 call, got %d", r.calls)
	}
	sv.ReconcileOnce(context.Background())
	if r.calls != 1 {
		t.Fatalf("second reconcile within backoff: expected still 1 call, got %d", r.calls)
	}
}

func TestSupervisor_RespawnDisabledWhenNoRespawner(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{}}
	store := &fakeSessionStore{sessions: nil}
	em := &fakeEmitter{}

	sv := supervisor.NewWithRunner(store, em, 50*time.Millisecond, tmux)
	// No SetJournalRespawner — should be a no-op.
	sv.ReconcileOnce(context.Background())
}

func TestSupervisor_DeadJournalRowEmitsPidExitAndRespawns(t *testing.T) {
	tmux := &fakeTmuxRunner{windowsBySession: map[string][]string{"gru-journal": {}}}
	store := &fakeSessionStore{sessions: []supervisor.LiveSession{{
		ID: "journal-dead", TmuxSession: "gru-journal", TmuxWindow: "journal·zzz",
		Status: "running", Role: "assistant",
	}}}
	em := &fakeEmitter{}
	r := &fakeRespawner{}

	sv := supervisor.NewWithRunner(store, em, 50*time.Millisecond, tmux)
	sv.SetJournalRespawner(r)

	sv.ReconcileOnce(context.Background())
	if len(em.emitted) != 1 || em.emitted[0].SessionID != "journal-dead" {
		t.Fatalf("expected pid_exit event for dead journal row, got %v", em.emitted)
	}
	if r.calls != 1 {
		t.Fatalf("expected respawn call after journal row dies, got %d", r.calls)
	}
}
