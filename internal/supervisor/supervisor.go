package supervisor

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

type LiveSession struct {
	ID          string
	TmuxSession string
	TmuxWindow  string
	Status      string
	Role        string
}

type StatusUpdate struct {
	SessionID string
	Status    string
}

type ExitEvent struct {
	SessionID   string
	TmuxSession string
	TmuxWindow  string
	NewStatus   string // "errored" or "completed"
}

type SessionStore interface {
	ListLiveSessions(ctx context.Context) ([]LiveSession, error)
	UpdateSessionStatus(ctx context.Context, sessionID, status string) error
}

type EventPublisher interface {
	PublishExit(ctx context.Context, e ExitEvent)
}

type tmuxOutputRunner interface {
	Output(args ...string) ([]byte, error)
}

type realTmuxRunner struct{}

func (r *realTmuxRunner) Output(args ...string) ([]byte, error) {
	return exec.Command("tmux", args...).Output()
}

// JournalRespawner respawns the journal session after its previous tmux window
// is gone. Returning a non-nil error backs the supervisor off before trying
// again. Can be nil if journal respawn is not configured.
type JournalRespawner interface {
	RespawnJournal(ctx context.Context) error
}

type Supervisor struct {
	store    SessionStore
	pub      EventPublisher
	interval time.Duration
	tmux     tmuxOutputRunner

	journal        JournalRespawner
	nextJournalTry time.Time // minimum wall time for the next respawn attempt
	journalBackoff time.Duration
}

func New(store SessionStore, pub EventPublisher, interval time.Duration) *Supervisor {
	return &Supervisor{store: store, pub: pub, interval: interval, tmux: &realTmuxRunner{}}
}

func NewWithRunner(store SessionStore, pub EventPublisher, interval time.Duration, tmux tmuxOutputRunner) *Supervisor {
	return &Supervisor{store: store, pub: pub, interval: interval, tmux: tmux}
}

// SetJournalRespawner wires the journal-respawn hook. If nil, journal sessions
// are treated like any other session (marked errored when their tmux window
// disappears).
func (s *Supervisor) SetJournalRespawner(r JournalRespawner) { s.journal = r }

// windowAlive reports whether the window still exists AND has at least one
// live pane. We set remain-on-exit=on on session windows so users can read the
// final output, which means a crashed agent leaves a dead pane behind — the
// window is still listed but the process inside has exited. Treat that as not
// alive so supervisor marks the session errored (and respawns the journal).
func (s *Supervisor) windowAlive(tmuxSession, tmuxWindow string) bool {
	target := tmuxSession + ":" + tmuxWindow
	out, err := s.tmux.Output("list-panes", "-t", target, "-F", "#{pane_dead}")
	if err != nil {
		return false // window gone entirely, or tmux session gone
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
		if sess.TmuxSession == "" || sess.TmuxWindow == "" {
			continue
		}
		if s.windowAlive(sess.TmuxSession, sess.TmuxWindow) {
			if sess.Role == "journal" {
				journalAlive = true
			}
			continue
		}
		// Sessions that were idle/needs_attention completed normally;
		// running/starting sessions crashed.
		newStatus := "errored"
		if sess.Status == "idle" || sess.Status == "needs_attention" {
			newStatus = "completed"
		}
		if err := s.store.UpdateSessionStatus(ctx, sess.ID, newStatus); err != nil {
			continue
		}
		s.pub.PublishExit(ctx, ExitEvent{
			SessionID:   sess.ID,
			TmuxSession: sess.TmuxSession,
			TmuxWindow:  sess.TmuxWindow,
			NewStatus:   newStatus,
		})
	}

	if s.journal != nil && !journalAlive {
		s.tryRespawnJournal(ctx)
	}
}

// tryRespawnJournal respawns the journal session if backoff has elapsed. It
// escalates the backoff on failure (5s → 15s → 60s) and resets it on success.
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
