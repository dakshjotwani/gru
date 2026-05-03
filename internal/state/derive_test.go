package state_test

import (
	"encoding/json"
	"testing"

	"github.com/dakshjotwani/gru/internal/ingest"
	"github.com/dakshjotwani/gru/internal/state"
)

// boolPtr is a tiny helper for the Ok / Graceful pointer fields.
func boolPtr(b bool) *bool { return &b }

func TestDerive_TurnStartedFlipsToRunning(t *testing.T) {
	st, projs := state.Derive(state.Initial(), ingest.Event{
		Type: ingest.TypeTurnStarted, Trigger: "user_prompt", Ts: "2026-05-02T18:00:00Z",
	})
	if st.Status != state.StatusRunning {
		t.Fatalf("status = %s, want running", st.Status)
	}
	wantProjections(t, projs, "turn.started", "session.transition")
	wantTransition(t, projs, state.StatusStarting, state.StatusRunning)
}

func TestDerive_TurnEndedFlipsToIdle(t *testing.T) {
	prev := state.State{Status: state.StatusRunning}
	st, projs := state.Derive(prev, ingest.Event{
		Type: ingest.TypeTurnEnded, StopReason: "end_turn",
	})
	if st.Status != state.StatusIdle {
		t.Fatalf("status = %s, want idle", st.Status)
	}
	if st.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q", st.StopReason)
	}
	wantProjections(t, projs, "turn.ended", "session.transition")
}

func TestDerive_TurnEndedToolUseStaysRunning(t *testing.T) {
	// stop_reason=tool_use means Claude paused for a tool — it'll
	// resume; we should NOT flip to idle.
	prev := state.State{Status: state.StatusRunning}
	st, projs := state.Derive(prev, ingest.Event{
		Type: ingest.TypeTurnEnded, StopReason: "tool_use",
	})
	if st.Status != state.StatusRunning {
		t.Fatalf("status = %s, want running (tool_use is not a real stop)", st.Status)
	}
	wantProjections(t, projs, "turn.ended")
}

func TestDerive_ToolCompletedDoesNotFlipStatus(t *testing.T) {
	prev := state.State{Status: state.StatusRunning}
	st, projs := state.Derive(prev, ingest.Event{
		Type: ingest.TypeToolCompleted, Tool: "Bash", Ok: boolPtr(true),
	})
	if st.Status != state.StatusRunning {
		t.Fatalf("tool_completed should not flip status; got %s", st.Status)
	}
	wantProjections(t, projs, "tool.completed")
}

// After a permission_prompt is approved, Claude resumes by running
// the tool — there is no UserPromptSubmit between the approval and
// the next PostToolUse. tool_completed must unstick the session
// from needs_attention.
func TestDerive_ToolCompletedExitsNeedsAttention(t *testing.T) {
	prev := state.State{Status: state.StatusNeedsAttention}
	st, projs := state.Derive(prev, ingest.Event{
		Type: ingest.TypeToolCompleted, Tool: "Bash", Ok: boolPtr(true),
	})
	if st.Status != state.StatusRunning {
		t.Fatalf("status = %s, want running", st.Status)
	}
	wantProjections(t, projs, "tool.completed", "session.transition")
	wantTransition(t, projs, state.StatusNeedsAttention, state.StatusRunning)
}

func TestDerive_AttentionRequestedFlipsToNeedsAttention(t *testing.T) {
	prev := state.State{Status: state.StatusIdle}
	st, projs := state.Derive(prev, ingest.Event{
		Type: ingest.TypeAttentionRequested, Reason: "idle_prompt",
	})
	if st.Status != state.StatusNeedsAttention {
		t.Fatalf("status = %s", st.Status)
	}
	wantTransition(t, projs, state.StatusIdle, state.StatusNeedsAttention)
	for _, p := range projs {
		if p.Type == "session.transition" {
			var body map[string]string
			_ = json.Unmarshal(p.Payload, &body)
			if body["why"] != "notification:idle_prompt" {
				t.Errorf("why = %q", body["why"])
			}
		}
	}
}

func TestDerive_ProcessExitedGraceful(t *testing.T) {
	prev := state.State{Status: state.StatusIdle}
	st, _ := state.Derive(prev, ingest.Event{
		Type: ingest.TypeProcessExited, Graceful: boolPtr(true),
	})
	if st.Status != state.StatusCompleted {
		t.Fatalf("graceful exit should -> completed; got %s", st.Status)
	}
}

func TestDerive_ProcessExitedNotGraceful(t *testing.T) {
	prev := state.State{Status: state.StatusRunning}
	st, _ := state.Derive(prev, ingest.Event{
		Type: ingest.TypeProcessExited, Graceful: boolPtr(false),
	})
	if st.Status != state.StatusErrored {
		t.Fatalf("non-graceful exit should -> errored; got %s", st.Status)
	}
}

func TestDerive_ProcessExitedRespectsTerminal(t *testing.T) {
	// If the user already KillSession'd the agent, a later
	// process_exited should not flip killed -> completed/errored.
	prev := state.State{Status: state.StatusKilled}
	st, projs := state.Derive(prev, ingest.Event{
		Type: ingest.TypeProcessExited, Graceful: boolPtr(true),
	})
	if st.Status != state.StatusKilled {
		t.Fatalf("terminal status must not be overwritten; got %s", st.Status)
	}
	if len(projs) != 0 {
		t.Errorf("no projections expected on terminal-respect; got %v", projs)
	}
}

func TestDerive_KilledByUser(t *testing.T) {
	prev := state.State{Status: state.StatusRunning}
	st, projs := state.Derive(prev, ingest.Event{Type: ingest.TypeKilledByUser})
	if st.Status != state.StatusKilled {
		t.Fatalf("status = %s", st.Status)
	}
	wantProjections(t, projs, "killed.by_user", "session.transition")
}

func TestDerive_UnknownDoesNotFlipStatus(t *testing.T) {
	prev := state.State{Status: state.StatusRunning}
	st, projs := state.Derive(prev, ingest.Event{
		Type: ingest.TypeUnknown, ClaudeEvent: "PreToolUse",
	})
	if st.Status != state.StatusRunning {
		t.Fatalf("unknown event should not flip status; got %s", st.Status)
	}
	wantProjections(t, projs, "unknown")
}

func TestDerive_LastEventAtAdvances(t *testing.T) {
	prev := state.State{Status: state.StatusIdle, LastEventAt: "2026-05-01T00:00:00Z"}
	st, _ := state.Derive(prev, ingest.Event{
		Type: ingest.TypeToolCompleted, Ts: "2026-05-02T12:34:56Z",
	})
	if st.LastEventAt != "2026-05-02T12:34:56Z" {
		t.Errorf("last_event_at = %q", st.LastEventAt)
	}
}

// ── helpers ───────────────────────────────────────────────────────────

func wantProjections(t *testing.T, got []state.Projected, wantTypes ...string) {
	t.Helper()
	if len(got) != len(wantTypes) {
		t.Fatalf("got %d projections (%v), want %d (%v)", len(got), projTypes(got), len(wantTypes), wantTypes)
	}
	for i, want := range wantTypes {
		if got[i].Type != want {
			t.Errorf("projection %d = %q, want %q", i, got[i].Type, want)
		}
	}
}

func wantTransition(t *testing.T, got []state.Projected, from, to state.Status) {
	t.Helper()
	for _, p := range got {
		if p.Type != "session.transition" {
			continue
		}
		var body map[string]string
		_ = json.Unmarshal(p.Payload, &body)
		if body["from"] != string(from) || body["to"] != string(to) {
			t.Errorf("transition body = %v, want %s -> %s", body, from, to)
		}
		return
	}
	t.Errorf("no session.transition found in %v", projTypes(got))
}

func projTypes(p []state.Projected) []string {
	out := make([]string, len(p))
	for i, v := range p {
		out[i] = v.Type
	}
	return out
}
