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

type Supervisor struct {
	store    SessionStore
	pub      EventPublisher
	interval time.Duration
	tmux     tmuxOutputRunner
}

func New(store SessionStore, pub EventPublisher, interval time.Duration) *Supervisor {
	return &Supervisor{store: store, pub: pub, interval: interval, tmux: &realTmuxRunner{}}
}

func NewWithRunner(store SessionStore, pub EventPublisher, interval time.Duration, tmux tmuxOutputRunner) *Supervisor {
	return &Supervisor{store: store, pub: pub, interval: interval, tmux: tmux}
}

func (s *Supervisor) windowExists(tmuxSession, tmuxWindow string) bool {
	out, err := s.tmux.Output("list-windows", "-t", tmuxSession, "-F", "#{window_name}")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), tmuxWindow)
}

func (s *Supervisor) ReconcileOnce(ctx context.Context) {
	sessions, err := s.store.ListLiveSessions(ctx)
	if err != nil {
		return
	}
	for _, sess := range sessions {
		if sess.TmuxSession == "" || sess.TmuxWindow == "" {
			continue
		}
		if s.windowExists(sess.TmuxSession, sess.TmuxWindow) {
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
