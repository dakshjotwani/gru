// Package supervisor is a tmux-pane liveness probe. It does NOT write
// session.status — that's the tailer's job. When a pane disappears
// the supervisor emits a synthetic `claude_pid_exit` event into the
// events projection (via the publisher's tailer-event injection); the
// derivation function turns that into the right terminal status.
package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dakshjotwani/gru/internal/state"
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

// EventEmitter is how the supervisor injects synthetic events into the
// projection. The tailer picks these up via the standard derivation
// path, so the supervisor never needs to know about session statuses.
type EventEmitter interface {
	EmitSupervisorEvent(ctx context.Context, sessionID, projectID, runtime string, payload []byte) error
}

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
	emitter  EventEmitter
	interval time.Duration
	tmux     tmuxOutputRunner

	mu      sync.Mutex
	emitted map[string]bool // sessions we've already emitted pid_exit for

	journal        JournalRespawner
	nextJournalTry time.Time
	journalBackoff time.Duration
}

// New constructs a supervisor.
func New(s SessionStore, e EventEmitter, interval time.Duration) *Supervisor {
	return &Supervisor{
		store:    s,
		emitter:  e,
		interval: interval,
		tmux:     &realTmuxRunner{},
		emitted:  make(map[string]bool),
	}
}

// NewWithRunner is the test-friendly constructor.
func NewWithRunner(s SessionStore, e EventEmitter, interval time.Duration, tmux tmuxOutputRunner) *Supervisor {
	return &Supervisor{
		store:    s,
		emitter:  e,
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
		if err := s.emitter.EmitSupervisorEvent(ctx, sess.ID, "", "supervisor", payload); err != nil {
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

// ── file-based emitter: supervisor → ~/.gru/supervisor/<sid>.jsonl ───

// FileEmitter appends supervisor events to a per-session JSONL file
// under <homeDir>/.gru/supervisor/. The session's tailer also watches
// this file (as state.SourceSupervisor) so the derivation function
// turns claude_pid_exit into the right terminal status.
//
// File-based delivery keeps the supervisor decoupled from the
// publisher/store and matches the spec's "producer never makes
// network calls" property.
type FileEmitter struct {
	dir string
}

// NewFileEmitter constructs a FileEmitter that writes under
// <homeDir>/.gru/supervisor/. The directory is created on first
// emit; callers should not pre-create it.
func NewFileEmitter(homeDir string) *FileEmitter {
	return &FileEmitter{dir: filepath.Join(homeDir, ".gru", "supervisor")}
}

func (e *FileEmitter) EmitSupervisorEvent(_ context.Context, sessionID, _, _ string, payload []byte) error {
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(e.dir, sessionID+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("supervisor emit: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("supervisor emit: write: %w", err)
	}
	return nil
}

// Compile-time interface check.
var _ EventEmitter = (*FileEmitter)(nil)

// ── compile-time helpers (avoid unused-import warnings) ──────────────

var _ = state.SourceSupervisor // referenced by the design contract
