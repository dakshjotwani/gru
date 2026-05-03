// Package state implements the per-session state-derivation function:
// a deterministic fold over the gru-owned event log
// (~/.gru/events/<sid>.jsonl) into the sessions row + projected event
// rows. Lives entirely in pure-Go space — no I/O, no globals.
//
// Design contract: same input events → same output state. That
// property is what lets the tailer wipe the events projection and
// replay from byte 0 on every server start.
//
// Rev 3: input is gru's own typed event grammar (see internal/ingest).
// The translator at the ingest boundary owns Anthropic's hook schema;
// derivation never sees Claude-format JSON.
package state

import (
	"encoding/json"
	"time"

	"github.com/dakshjotwani/gru/internal/ingest"
)

// Status mirrors the textual session status used in the SQLite
// sessions row and in protobuf SessionStatus.
type Status string

const (
	StatusStarting       Status = "starting"
	StatusRunning        Status = "running"
	StatusIdle           Status = "idle"
	StatusNeedsAttention Status = "needs_attention"
	StatusCompleted      Status = "completed"
	StatusErrored        Status = "errored"
	StatusKilled         Status = "killed"
)

// State is what the derivation function carries across events. It is
// not what we persist directly — the tailer projects it into the
// sessions row + events rows on commit.
//
// Smaller than rev-2's State: pendingToolUseIDs and PermissionMode
// are gone (no hook surface for those, and rev-3 derivation doesn't
// need them — Stop hooks are authoritative for "turn ended").
type State struct {
	Status      Status
	StopReason  string
	LastEventAt string // RFC3339; written by the most recent event
}

// Initial returns the state for a freshly-launched session — what the
// tailer starts with before any events.
func Initial() State {
	return State{Status: StatusStarting}
}

// Projected is one row the derivation function asks the tailer to
// write into the events projection. The tailer wraps the slice in a
// single SQL transaction with the sessions row update.
type Projected struct {
	Type      string
	Timestamp string // RFC3339
	Payload   []byte
}

// Derive applies one event to prev and returns the resulting state
// plus zero or more projected event rows. The function is pure: same
// inputs always produce the same outputs.
//
// When status flips, Derive emits a session.transition projection
// alongside the type-specific projection. The frontend trusts
// session.transition as the single source of truth for status
// changes; per-type projections feed the dashboard's recent-events
// ring buffer.
func Derive(prev State, ev ingest.Event) (State, []Projected) {
	next := prev
	if ev.Ts != "" {
		next.LastEventAt = ev.Ts
	}

	var typed Projected // type-specific projection (always emitted unless explicitly dropped)
	typed.Timestamp = chooseTs(ev.Ts)
	typed.Payload = mustEncode(ev)

	switch ev.Type {
	case ingest.TypeSessionStarted:
		next.Status = StatusStarting
		typed.Type = "session.started"

	case ingest.TypeTurnStarted:
		next.Status = StatusRunning
		typed.Type = "turn.started"

	case ingest.TypeTurnEnded:
		next.StopReason = ev.StopReason
		// stop_reason "tool_use" means the turn paused for a tool — Claude
		// will resume; we stay running. Otherwise the turn is genuinely
		// done.
		if ev.StopReason == "tool_use" {
			next.Status = StatusRunning
		} else {
			next.Status = StatusIdle
		}
		typed.Type = "turn.ended"

	case ingest.TypeToolCompleted:
		// A tool returned. Claude is still mid-turn; the next Stop
		// hook will flip us to idle. If we were in needs_attention
		// (e.g. a permission_prompt the user just approved), the
		// fact that a tool ran means the agent is unblocked —
		// flip back to running. Otherwise keep status unchanged.
		if prev.Status == StatusNeedsAttention {
			next.Status = StatusRunning
		}
		typed.Type = "tool.completed"

	case ingest.TypeAttentionRequested:
		next.Status = StatusNeedsAttention
		typed.Type = "attention.requested"

	case ingest.TypeProcessExited:
		// Don't overwrite an already-terminal status. Replay of a
		// persisted process_exited event after a KillSession would
		// otherwise flip killed → completed.
		if isTerminal(prev.Status) {
			return next, nil
		}
		if ev.Graceful != nil && *ev.Graceful {
			next.Status = StatusCompleted
		} else {
			next.Status = StatusErrored
		}
		typed.Type = "process.exited"

	case ingest.TypeKilledByUser:
		next.Status = StatusKilled
		typed.Type = "killed.by_user"

	case ingest.TypeUnknown:
		// No status flip. Project for visibility.
		typed.Type = "unknown"

	default:
		// Shouldn't happen — translator emits a closed enum — but be
		// defensive and skip rather than mutate state on garbage.
		return next, nil
	}

	out := []Projected{typed}
	if next.Status != prev.Status {
		out = append(out, transitionProjection(prev.Status, next.Status, typed.Timestamp, sourceLabel(ev)))
	}
	return next, out
}

// transitionProjection synthesizes the session.transition row the
// frontend trusts for status changes. {from, to, why} matches the
// rev-2 shape so useSessionStream.ts doesn't need updating.
func transitionProjection(from, to Status, ts, why string) Projected {
	body, _ := json.Marshal(map[string]string{
		"from": string(from),
		"to":   string(to),
		"why":  why,
	})
	return Projected{Type: "session.transition", Timestamp: ts, Payload: body}
}

// sourceLabel describes the ingest event in the session.transition
// `why` field. Useful for debugging which signal flipped status.
func sourceLabel(ev ingest.Event) string {
	switch ev.Type {
	case ingest.TypeAttentionRequested:
		return "notification:" + ev.Reason
	case ingest.TypeProcessExited:
		return "supervisor:process_exited"
	case ingest.TypeKilledByUser:
		return "cli:killed"
	default:
		return "claude:" + string(ev.Type)
	}
}

func chooseTs(ts string) string {
	if ts != "" {
		return ts
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func mustEncode(ev ingest.Event) []byte {
	b, err := json.Marshal(ev)
	if err != nil {
		// Should never happen for our typed Event; fall back to an
		// empty object rather than panicking the tailer.
		return []byte("{}")
	}
	return b
}

func isTerminal(s Status) bool {
	switch s {
	case StatusCompleted, StatusErrored, StatusKilled:
		return true
	}
	return false
}
