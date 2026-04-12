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

type CrashEvent struct {
	SessionID   string
	TmuxSession string
	TmuxWindow  string
}

type SessionStore interface {
	ListLiveSessions(ctx context.Context) ([]LiveSession, error)
	MarkSessionErrored(ctx context.Context, sessionID string) error
}

type EventPublisher interface {
	PublishCrash(ctx context.Context, e CrashEvent)
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
		if err := s.store.MarkSessionErrored(ctx, sess.ID); err != nil {
			continue
		}
		s.pub.PublishCrash(ctx, CrashEvent{
			SessionID:   sess.ID,
			TmuxSession: sess.TmuxSession,
			TmuxWindow:  sess.TmuxWindow,
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
