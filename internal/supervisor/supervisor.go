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
	// LastEventAt is the time of the most recent event for this session.
	// Nil if no events have been recorded yet.
	LastEventAt *time.Time
	// LastEventType is the adapter.EventType string of the most recent event
	// (e.g. "tool.pre", "tool.post", "session.start"). Empty if unknown.
	// Used by the staleness heuristic to distinguish "inside a long-running
	// tool call" (last event was tool.pre) from "should have made progress
	// by now" (last event was tool.post, session.start, etc.).
	LastEventType string
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

// StatusChangeEvent signals an in-place status transition on a live session
// (e.g. running → needs_attention via the staleness heuristic). Unlike
// ExitEvent, the tmux window is still alive.
type StatusChangeEvent struct {
	SessionID string
	NewStatus string
}

type SessionStore interface {
	ListLiveSessions(ctx context.Context) ([]LiveSession, error)
	UpdateSessionStatus(ctx context.Context, sessionID, status string) error
}

type EventPublisher interface {
	PublishExit(ctx context.Context, e ExitEvent)
	PublishStatusChange(ctx context.Context, e StatusChangeEvent)
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

// AttentionRescorer recomputes the attention score for a live session on
// every supervisor tick. The real consumer is the attention engine's
// staleness ramp, which otherwise wouldn't drift without new hook events.
// Nil-safe: when unset, the supervisor skips rescoring.
type AttentionRescorer interface {
	Rescore(ctx context.Context, sessionID string)
}

// DefaultIdleThreshold is the age beyond which a running session with no new
// events is treated as stuck and flipped to needs_attention. Chosen to leave
// comfortable headroom above typical long tool calls (test suites, builds)
// while still surfacing genuinely stuck sessions within a reasonable window.
const DefaultIdleThreshold = 15 * time.Minute

type Supervisor struct {
	store         SessionStore
	pub           EventPublisher
	interval      time.Duration
	tmux          tmuxOutputRunner
	idleThreshold time.Duration
	now           func() time.Time // injectable for tests

	journal        JournalRespawner
	nextJournalTry time.Time // minimum wall time for the next respawn attempt
	journalBackoff time.Duration

	rescorer AttentionRescorer
}

func New(store SessionStore, pub EventPublisher, interval time.Duration) *Supervisor {
	return &Supervisor{
		store:         store,
		pub:           pub,
		interval:      interval,
		tmux:          &realTmuxRunner{},
		idleThreshold: DefaultIdleThreshold,
		now:           time.Now,
	}
}

func NewWithRunner(store SessionStore, pub EventPublisher, interval time.Duration, tmux tmuxOutputRunner) *Supervisor {
	return &Supervisor{
		store:         store,
		pub:           pub,
		interval:      interval,
		tmux:          tmux,
		idleThreshold: DefaultIdleThreshold,
		now:           time.Now,
	}
}

// SetIdleThreshold overrides the default staleness threshold. Set to 0 to
// disable the heuristic entirely.
func (s *Supervisor) SetIdleThreshold(d time.Duration) { s.idleThreshold = d }

// SetNowFunc overrides the wall-clock source; used by tests.
func (s *Supervisor) SetNowFunc(fn func() time.Time) { s.now = fn }

// SetJournalRespawner wires the journal-respawn hook. If nil, journal sessions
// are treated like any other session (marked errored when their tmux window
// disappears).
func (s *Supervisor) SetJournalRespawner(r JournalRespawner) { s.journal = r }

// SetAttentionRescorer wires the per-tick attention rescore hook. If nil, the
// attention engine's staleness ramp only advances when a hook event arrives.
func (s *Supervisor) SetAttentionRescorer(r AttentionRescorer) { s.rescorer = r }

// windowAlive reports whether the window still exists AND has at least one
// live pane. We set remain-on-exit=on on session windows so users can read the
// final output, which means a crashed agent leaves a dead pane behind — the
// window is still listed but the process inside has exited. Treat that as not
// alive so supervisor marks the session errored (and respawns the journal).
//
// When tmuxWindow is empty (v2 one-session-per-window layout), the target is
// the tmux session itself and list-panes enumerates all panes in the session.
func (s *Supervisor) windowAlive(tmuxSession, tmuxWindow string) bool {
	target := tmuxSession
	if tmuxWindow != "" {
		target = tmuxSession + ":" + tmuxWindow
	}
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
		// v1 sessions had a TmuxWindow; v2 (one-session-per-window) leaves it
		// empty and the target is the tmux session itself. Either way we
		// require at least a session name.
		if sess.TmuxSession == "" {
			continue
		}
		if s.windowAlive(sess.TmuxSession, sess.TmuxWindow) {
			if sess.Role == "assistant" {
				journalAlive = true
			}
			s.checkStale(ctx, sess)
			if s.rescorer != nil {
				s.rescorer.Rescore(ctx, sess.ID)
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

// checkStale flips a live `running` session to `needs_attention` when no
// event has arrived within the idle threshold, unless the session is
// currently inside a tool call (last event was tool.pre) — in which case
// silence is legitimate and we leave it alone. Only `running` is considered;
// `idle`, `needs_attention`, and `starting` are left untouched. This is the
// safety net for dropped hook deliveries, because once hooks resume flowing
// Claude Code's own idle_prompt notification will re-flag correctly.
func (s *Supervisor) checkStale(ctx context.Context, sess LiveSession) {
	if s.idleThreshold <= 0 {
		return
	}
	if sess.Status != "running" {
		return
	}
	if sess.LastEventAt == nil {
		return
	}
	if s.now().Sub(*sess.LastEventAt) < s.idleThreshold {
		return
	}
	// tool.pre with no matching tool.post means Claude is inside a tool
	// call (e.g. a long test run). Don't interrupt legitimate long work.
	if sess.LastEventType == "tool.pre" || sess.LastEventType == "subagent.start" {
		return
	}
	if err := s.store.UpdateSessionStatus(ctx, sess.ID, "needs_attention"); err != nil {
		return
	}
	s.pub.PublishStatusChange(ctx, StatusChangeEvent{
		SessionID: sess.ID,
		NewStatus: "needs_attention",
	})
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
