package state_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dakshjotwani/gru/internal/state"
)

// helper: fold a sequence of (source, line) pairs through Derive to get
// the resulting state and the projected events.
func fold(t *testing.T, lines []entry) (state.State, []state.Projected) {
	t.Helper()
	st := state.Initial()
	var got []state.Projected
	for i, e := range lines {
		var p *state.Projected
		st, p = state.Derive(st, e.src, []byte(e.line))
		if p != nil {
			got = append(got, *p)
		}
		if e.assert != nil {
			e.assert(t, i, st)
		}
	}
	return st, got
}

type entry struct {
	src    state.Source
	line   string
	assert func(t *testing.T, idx int, st state.State)
}

// ── transcript: simple turn boundary cases ───────────────────────────

func TestDerive_assistantEndTurnFlipsToIdle(t *testing.T) {
	st, projs := fold(t, []entry{
		{src: state.SourceTranscript,
			line: `{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}`,
		},
	})
	if st.Status != state.StatusIdle {
		t.Fatalf("status = %q, want idle", st.Status)
	}
	if st.ClaudeStopReason != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", st.ClaudeStopReason)
	}
	if len(projs) != 1 || projs[0].Type != "assistant.message" {
		t.Fatalf("projections = %+v, want one assistant.message", projs)
	}
}

func TestDerive_assistantToolUseStaysRunning(t *testing.T) {
	st, _ := fold(t, []entry{
		{src: state.SourceTranscript,
			line: `{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_abc"}]}}`,
		},
	})
	if st.Status != state.StatusRunning {
		t.Fatalf("status = %q, want running", st.Status)
	}
	if _, ok := st.PendingToolUseIDs["toolu_abc"]; !ok {
		t.Fatalf("expected pending tool_use id 'toolu_abc', got %v", st.PendingToolUseIDs)
	}
}

func TestDerive_userToolResultClearsPendingThenIdleOnNextEndTurn(t *testing.T) {
	st, _ := fold(t, []entry{
		{src: state.SourceTranscript,
			line: `{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_xyz"}]}}`,
			assert: func(t *testing.T, _ int, st state.State) {
				if st.Status != state.StatusRunning {
					t.Fatalf("after tool_use status = %q, want running", st.Status)
				}
			},
		},
		{src: state.SourceTranscript,
			line: `{"type":"user","timestamp":"2026-04-25T00:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_xyz"}]}}`,
		},
		{src: state.SourceTranscript,
			line: `{"type":"assistant","timestamp":"2026-04-25T00:00:02Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}`,
		},
	})
	if st.Status != state.StatusIdle {
		t.Fatalf("final status = %q, want idle", st.Status)
	}
	if len(st.PendingToolUseIDs) != 0 {
		t.Fatalf("pending tool_use ids = %v, want empty", st.PendingToolUseIDs)
	}
}

// ── transcript: noise and unknown types ──────────────────────────────

func TestDerive_noiseTypesIgnored(t *testing.T) {
	noise := []string{
		`{"type":"file-history-snapshot"}`,
		`{"type":"attachment"}`,
		`{"type":"worktree-state"}`,
		`{"type":"queue-operation"}`,
		`{"type":"pr-link"}`,
		`{"type":"hook"}`,
	}
	st := state.Initial()
	for _, line := range noise {
		var p *state.Projected
		st, p = state.Derive(st, state.SourceTranscript, []byte(line))
		if p != nil {
			t.Fatalf("expected no projection for noise line %q, got %+v", line, p)
		}
	}
	if st.Status != state.StatusStarting {
		t.Fatalf("status mutated by noise: got %q, want starting", st.Status)
	}
}

func TestDerive_malformedJSONIgnored(t *testing.T) {
	st := state.Initial()
	stNext, p := state.Derive(st, state.SourceTranscript, []byte(`{not valid json`))
	if p != nil {
		t.Fatalf("malformed line produced projection %+v, want nil", p)
	}
	if stNext.Status != st.Status {
		t.Fatalf("malformed line mutated status %q -> %q", st.Status, stNext.Status)
	}
}

func TestDerive_unknownTypeProjectedNotMutating(t *testing.T) {
	st, projs := fold(t, []entry{
		{src: state.SourceTranscript, line: `{"type":"some-future-thing","timestamp":"2026-04-25T00:00:00Z"}`},
	})
	if st.Status != state.StatusStarting {
		t.Fatalf("unknown type changed status to %q", st.Status)
	}
	if len(projs) != 1 || !strings.HasPrefix(projs[0].Type, "unknown.") {
		t.Fatalf("expected one unknown.* projection, got %+v", projs)
	}
}

// ── permission-mode tracking ─────────────────────────────────────────

func TestDerive_permissionModeRecorded(t *testing.T) {
	st, _ := fold(t, []entry{
		{src: state.SourceTranscript, line: `{"type":"permission-mode","permissionMode":"plan"}`},
	})
	if st.PermissionMode != "plan" {
		t.Fatalf("permission_mode = %q, want plan", st.PermissionMode)
	}
}

func TestDerive_permissionModeTransition(t *testing.T) {
	st, _ := fold(t, []entry{
		{src: state.SourceTranscript, line: `{"type":"permission-mode","permissionMode":"default"}`},
		{src: state.SourceTranscript, line: `{"type":"permission-mode","permissionMode":"plan"}`},
		{src: state.SourceTranscript, line: `{"type":"permission-mode","permissionMode":"acceptEdits"}`},
	})
	if st.PermissionMode != "acceptEdits" {
		t.Fatalf("final permission_mode = %q, want acceptEdits", st.PermissionMode)
	}
}

// ── notification path ────────────────────────────────────────────────

func TestDerive_permissionNotificationFlipsToNeedsAttention(t *testing.T) {
	st, projs := fold(t, []entry{
		{src: state.SourceTranscript, line: `{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"x"}]}}`},
		{src: state.SourceNotification, line: `{"hook_event_name":"Notification","notification_type":"permission_prompt"}`},
	})
	if st.Status != state.StatusNeedsAttention {
		t.Fatalf("status = %q, want needs_attention", st.Status)
	}
	if st.AttentionScore != 1.0 {
		t.Fatalf("attention_score = %v, want 1.0", st.AttentionScore)
	}
	// Last projection should be a session.transition
	if len(projs) == 0 {
		t.Fatalf("expected projections, got none")
	}
	last := projs[len(projs)-1]
	if last.Type != "session.transition" {
		t.Fatalf("last projection type = %q, want session.transition", last.Type)
	}
	var body map[string]string
	if err := json.Unmarshal(last.Payload, &body); err != nil {
		t.Fatalf("transition payload not JSON: %v", err)
	}
	if body["to"] != "needs_attention" || body["from"] == "" {
		t.Fatalf("transition body = %+v, want from=*, to=needs_attention", body)
	}
}

func TestDerive_idlePromptNotification(t *testing.T) {
	st, _ := fold(t, []entry{
		{src: state.SourceNotification, line: `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`},
	})
	if st.Status != state.StatusNeedsAttention {
		t.Fatalf("idle_prompt did not set needs_attention; got %q", st.Status)
	}
}

func TestDerive_unknownNotificationTypeNoStatusChange(t *testing.T) {
	st, _ := fold(t, []entry{
		{src: state.SourceNotification, line: `{"hook_event_name":"Notification","notification_type":"informational"}`},
	})
	if st.Status != state.StatusStarting {
		t.Fatalf("unknown notification_type changed status to %q", st.Status)
	}
}

// ── supervisor path ──────────────────────────────────────────────────

func TestDerive_supervisorPidExitRunningToErrored(t *testing.T) {
	st := state.Initial()
	st.Status = state.StatusRunning
	st, p := state.Derive(st, state.SourceSupervisor,
		[]byte(`{"kind":"claude_pid_exit"}`))
	if st.Status != state.StatusErrored {
		t.Fatalf("running pane gone -> status %q, want errored", st.Status)
	}
	if p == nil || p.Type != "session.transition" {
		t.Fatalf("expected session.transition projection, got %+v", p)
	}
	if st.AttentionScore != 0 {
		t.Fatalf("attention_score after exit = %v, want 0", st.AttentionScore)
	}
}

func TestDerive_supervisorPidExitIdleToCompleted(t *testing.T) {
	st := state.Initial()
	st.Status = state.StatusIdle
	st, _ = state.Derive(st, state.SourceSupervisor,
		[]byte(`{"kind":"claude_pid_exit"}`))
	if st.Status != state.StatusCompleted {
		t.Fatalf("idle pane gone -> status %q, want completed", st.Status)
	}
}

// ── determinism: same inputs → same outputs (regression for §3.2) ────

func TestDerive_replayFromZeroProducesSameFinalState(t *testing.T) {
	lines := []entry{
		{src: state.SourceTranscript, line: `{"type":"permission-mode","permissionMode":"default"}`},
		{src: state.SourceTranscript, line: `{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"a"}]}}`},
		{src: state.SourceTranscript, line: `{"type":"user","timestamp":"2026-04-25T00:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"a"}]}}`},
		{src: state.SourceTranscript, line: `{"type":"assistant","timestamp":"2026-04-25T00:00:02Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}`},
		{src: state.SourceTranscript, line: `{"type":"file-history-snapshot"}`},
		{src: state.SourceTranscript, line: `{"type":"system","subtype":"stop_hook_summary","timestamp":"2026-04-25T00:00:03Z"}`},
	}
	first, _ := fold(t, lines)
	second, _ := fold(t, lines)

	if first.Status != second.Status ||
		first.PermissionMode != second.PermissionMode ||
		first.ClaudeStopReason != second.ClaudeStopReason {
		t.Fatalf("non-deterministic fold:\n  first  = %+v\n  second = %+v", first, second)
	}
	if first.Status != state.StatusIdle {
		t.Fatalf("expected idle final state, got %q", first.Status)
	}
}

// ── property: fold over 1000 lines completes and is deterministic ────

func TestDerive_largeFoldDeterministic(t *testing.T) {
	// Build a synthetic transcript: alternating tool_use / tool_result
	// pairs with periodic end_turn closures. Mimics realistic load.
	var lines []entry
	for i := 0; i < 1000; i++ {
		switch i % 4 {
		case 0:
			lines = append(lines, entry{src: state.SourceTranscript,
				line: `{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t` + itoa(i) + `"}]}}`,
			})
		case 1:
			lines = append(lines, entry{src: state.SourceTranscript,
				line: `{"type":"user","timestamp":"2026-04-25T00:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"t` + itoa(i-1) + `"}]}}`,
			})
		case 2:
			lines = append(lines, entry{src: state.SourceTranscript,
				line: `{"type":"file-history-snapshot"}`,
			})
		case 3:
			lines = append(lines, entry{src: state.SourceTranscript,
				line: `{"type":"assistant","timestamp":"2026-04-25T00:00:02Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}`,
			})
		}
	}
	a, _ := fold(t, lines)
	b, _ := fold(t, lines)
	if a.Status != b.Status {
		t.Fatalf("fold non-deterministic over 1000 lines: %q vs %q", a.Status, b.Status)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
