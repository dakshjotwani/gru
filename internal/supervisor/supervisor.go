// Package supervisor is a tmux-pane liveness probe. It does NOT write
// session.status — that's the tailer's job. When a pane disappears
// the supervisor emits a synthetic `claude_pid_exit` event through a
// caller-supplied EventSink; production wiring routes that to the
// matching session's tailer (which feeds it through the standard
// derivation path). The supervisor and the tailers live in the same
// gru server process, so no on-disk IPC is involved.
package supervisor

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type LiveSession struct {
	ID          string
	TmuxSession string
	TmuxWindow  string
	Status      string
	Role        string
}

type SessionStore interface {
	ListLiveSessions(ctx context.Context) ([]LiveSession, error)
}

// EventSink delivers a synthetic supervisor event to the right
// per-session tailer. The tailer feeds the payload through the
// standard derivation path, so the supervisor never needs to know
// about session statuses or care how the sink reaches the tailer.
//
// Production wiring is `tailerMgr.DispatchSupervisorEvent` (see
// cmd/gru/server.go); tests pass a function literal that records
// payloads for assertion.
type EventSink func(sessionID string, payload []byte) error

type tmuxOutputRunner interface {
	Output(args ...string) ([]byte, error)
}

type realTmuxRunner struct{}

func (r *realTmuxRunner) Output(args ...string) ([]byte, error) {
	return exec.Command("tmux", args...).Output()
}

// JournalRespawner respawns the journal session after its tmux window
// is gone. Unchanged from rev 1: it's a control-plane concern.
type JournalRespawner interface {
	RespawnJournal(ctx context.Context) error
}

type Supervisor struct {
	store    SessionStore
	sink     EventSink
	interval time.Duration
	tmux     tmuxOutputRunner

	mu      sync.Mutex
	emitted map[string]bool // sessions we've already emitted pid_exit for

	journal        JournalRespawner
	nextJournalTry time.Time
	journalBackoff time.Duration
}

// New constructs a supervisor.
func New(s SessionStore, sink EventSink, interval time.Duration) *Supervisor {
	return &Supervisor{
		store:    s,
		sink:     sink,
		interval: interval,
		tmux:     &realTmuxRunner{},
		emitted:  make(map[string]bool),
	}
}

// NewWithRunner is the test-friendly constructor.
func NewWithRunner(s SessionStore, sink EventSink, interval time.Duration, tmux tmuxOutputRunner) *Supervisor {
	return &Supervisor{
		store:    s,
		sink:     sink,
		interval: interval,
		tmux:     tmux,
		emitted:  make(map[string]bool),
	}
}

// SetJournalRespawner wires the journal-respawn hook.
func (s *Supervisor) SetJournalRespawner(r JournalRespawner) { s.journal = r }

// windowAlive reports whether the window still exists AND has at
// least one live pane. We set remain-on-exit=on on session windows so
// users can read the final output, which means a crashed agent leaves
// a dead pane behind — the window is still listed but the process
// inside has exited.
func (s *Supervisor) windowAlive(tmuxSession, tmuxWindow string) bool {
	target := tmuxSession
	if tmuxWindow != "" {
		target = tmuxSession + ":" + tmuxWindow
	}
	out, err := s.tmux.Output("list-panes", "-t", target, "-F", "#{pane_dead}")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "0" {
			return true
		}
	}
	return false
}

func (s *Supervisor) ReconcileOnce(ctx context.Context) {
	sessions, err := s.store.ListLiveSessions(ctx)
	if err != nil {
		return
	}
	journalAlive := false
	for _, sess := range sessions {
		if sess.TmuxSession == "" {
			continue
		}
		if s.windowAlive(sess.TmuxSession, sess.TmuxWindow) {
			if sess.Role == "assistant" {
				journalAlive = true
			}
			continue
		}
		// Pane gone: emit a synthetic claude_pid_exit event into the
		// projection. The tailer picks it up and lets the derivation
		// function decide whether this is errored or completed.
		s.mu.Lock()
		alreadyEmitted := s.emitted[sess.ID]
		if !alreadyEmitted {
			s.emitted[sess.ID] = true
		}
		s.mu.Unlock()
		if alreadyEmitted {
			continue
		}

		payload, _ := json.Marshal(map[string]interface{}{
			"kind":                "claude_pid_exit",
			"was_idle":            sess.Status == "idle",
			"was_needs_attention": sess.Status == "needs_attention",
			"tmux_session":        sess.TmuxSession,
		})
		if err := s.sink(sess.ID, payload); err != nil {
			// On error we leave alreadyEmitted=true; we'll retry next
			// tick by clearing the flag.
			s.mu.Lock()
			delete(s.emitted, sess.ID)
			s.mu.Unlock()
		}
	}

	if s.journal != nil && !journalAlive {
		s.tryRespawnJournal(ctx)
	}
}

// tryRespawnJournal respawns the journal session if backoff has
// elapsed.
func (s *Supervisor) tryRespawnJournal(ctx context.Context) {
	now := time.Now()
	if now.Before(s.nextJournalTry) {
		return
	}
	if err := s.journal.RespawnJournal(ctx); err != nil {
		s.journalBackoff = nextBackoff(s.journalBackoff)
		s.nextJournalTry = now.Add(s.journalBackoff)
		return
	}
	s.journalBackoff = 0
	s.nextJournalTry = time.Time{}
}

func nextBackoff(current time.Duration) time.Duration {
	switch {
	case current < 5*time.Second:
		return 5 * time.Second
	case current < 15*time.Second:
		return 15 * time.Second
	default:
		return 60 * time.Second
	}
}

func (s *Supervisor) Run(ctx context.Context) {
	s.ReconcileOnce(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ReconcileOnce(ctx)
		}
	}
}

