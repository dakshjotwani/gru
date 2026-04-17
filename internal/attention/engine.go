// Package attention computes per-session attention scores from the Claude
// Code hook event stream. The queue view in Gru's UI is ranked by this score,
// so what this package decides determines which session the operator looks at
// first when N are running.
//
// Signal model and weights come from docs/superpowers/specs/2026-04-17-gru-v2-design.md
// §Attention score. Weights are tunable via ~/.gru/server.yaml.
package attention

import (
	"encoding/json"
	"math"
	"sync"
	"time"
)

// Weights are the per-signal point values. All fields have sensible defaults;
// zero is treated as "use default", not "disable".
type Weights struct {
	Paused         float64
	Notification   float64
	ToolError      float64
	StalenessCap   float64
	StalenessStart time.Duration
	StalenessFull  time.Duration
}

// DefaultWeights are the values documented in the spec.
func DefaultWeights() Weights {
	return Weights{
		Paused:         1.0,
		Notification:   0.8,
		ToolError:      0.5,
		StalenessCap:   0.3,
		StalenessStart: 5 * time.Minute,
		StalenessFull:  15 * time.Minute,
	}
}

// Engine tracks per-session attention state and computes scores on hook
// events.
type Engine struct {
	weights Weights

	mu    sync.Mutex
	state map[string]*sessionState
	now   func() time.Time
}

// New returns a fresh Engine. Pass DefaultWeights() unless overriding.
func New(w Weights) *Engine {
	w = w.withDefaults()
	return &Engine{
		weights: w,
		state:   make(map[string]*sessionState),
		now:     time.Now,
	}
}

// SetNow overrides the wall-clock source; used by tests.
func (e *Engine) SetNow(fn func() time.Time) { e.now = fn }

// sessionState tracks the signals currently contributing for one session.
type sessionState struct {
	pausedActive       bool
	notificationActive bool
	toolErrorActive    bool
	lastEventAt        time.Time

	// For paused-detection. We track the last UserPromptSubmit (or ToolPost)
	// relative to Stop arrivals.
	lastHook           string
	pendingUserPrompt  bool // true after UserPromptSubmit until PreToolUse or PostToolUse arrives
}

// Snapshot is the engine's current view of a session's attention state.
type Snapshot struct {
	Score   float64           // non-negative, additive across signals
	Signals map[string]bool   // which signals are active right now
}

// JSON returns a compact JSON encoding of Signals suitable for
// `Session.attention_signals`.
func (s Snapshot) JSON() string {
	if len(s.Signals) == 0 {
		return ""
	}
	active := make([]string, 0, len(s.Signals))
	for k, v := range s.Signals {
		if v {
			active = append(active, k)
		}
	}
	if len(active) == 0 {
		return ""
	}
	buf, _ := json.Marshal(struct {
		Active []string `json:"active"`
	}{Active: active})
	return string(buf)
}

// OnEvent updates engine state based on a hook event type and returns the new
// Snapshot. eventType matches adapter.EventType strings:
//   - "session.start" / "tool.pre"                → clear all signals (agent working)
//   - "session.idle" (Stop hook)                  → paused (+paused signal)
//   - "tool.error"                                → +tool_error signal
//   - "notification.needs_attention"              → +notification signal
//   - "notification" (informational) / "tool.post"/"subagent.*" → no change
//
// Unknown event types are ignored (but lastEventAt is still advanced so
// staleness continues to track activity).
func (e *Engine) OnEvent(sessionID, eventType string) Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.state[sessionID]
	if !ok {
		st = &sessionState{}
		e.state[sessionID] = st
	}
	st.lastEventAt = e.now()

	switch eventType {
	case "session.start", "tool.pre", "subagent.start":
		st.pausedActive = false
		st.notificationActive = false
		st.toolErrorActive = false
	case "tool.error":
		st.toolErrorActive = true
	case "notification.needs_attention":
		st.notificationActive = true
	case "session.idle":
		// Stop hook fired: the agent finished its turn and is waiting for
		// the operator. That's the canonical "paused-awaiting-user" signal.
		st.pausedActive = true
	}
	st.lastHook = eventType

	return snapshotFromState(st, e.weights, e.now())
}

// Recompute returns a Snapshot without ingesting a new event. Call this
// periodically (e.g. every minute from supervisor) so the staleness ramp
// updates even when no events arrive.
func (e *Engine) Recompute(sessionID string) Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.state[sessionID]
	if !ok {
		return Snapshot{Signals: map[string]bool{}}
	}
	return snapshotFromState(st, e.weights, e.now())
}

// Forget drops per-session state. Call on session completion / kill.
func (e *Engine) Forget(sessionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.state, sessionID)
}

func snapshotFromState(st *sessionState, w Weights, now time.Time) Snapshot {
	sig := map[string]bool{}
	score := 0.0

	if st.pausedActive {
		sig["paused"] = true
		score += w.Paused
	}
	if st.notificationActive {
		sig["notification"] = true
		score += w.Notification
	}
	if st.toolErrorActive {
		sig["tool_error"] = true
		score += w.ToolError
	}
	if !st.lastEventAt.IsZero() {
		age := now.Sub(st.lastEventAt)
		if age >= w.StalenessStart {
			bonus := w.StalenessCap
			if age < w.StalenessFull {
				// Linear ramp in [StalenessStart, StalenessFull].
				frac := float64(age-w.StalenessStart) / float64(w.StalenessFull-w.StalenessStart)
				bonus = w.StalenessCap * frac
			}
			if bonus > 0 {
				sig["staleness"] = true
				score += bonus
			}
		}
	}

	if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 {
		score = 0
	}
	return Snapshot{Score: score, Signals: sig}
}

func (w Weights) withDefaults() Weights {
	d := DefaultWeights()
	if w.Paused == 0 {
		w.Paused = d.Paused
	}
	if w.Notification == 0 {
		w.Notification = d.Notification
	}
	if w.ToolError == 0 {
		w.ToolError = d.ToolError
	}
	if w.StalenessCap == 0 {
		w.StalenessCap = d.StalenessCap
	}
	if w.StalenessStart == 0 {
		w.StalenessStart = d.StalenessStart
	}
	if w.StalenessFull == 0 {
		w.StalenessFull = d.StalenessFull
	}
	return w
}
