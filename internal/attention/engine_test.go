package attention_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/attention"
)

func TestEngine_PausedFiresOnStop(t *testing.T) {
	e := attention.New(attention.DefaultWeights())
	snap := e.OnEvent("s1", "session.idle")
	if !snap.Signals["paused"] {
		t.Fatalf("expected paused signal, got %v", snap.Signals)
	}
	if snap.Score < 1.0 {
		t.Fatalf("expected score >= 1.0 for paused, got %v", snap.Score)
	}
}

func TestEngine_NotificationSignal(t *testing.T) {
	e := attention.New(attention.DefaultWeights())
	snap := e.OnEvent("s1", "notification.needs_attention")
	if !snap.Signals["notification"] {
		t.Fatalf("expected notification signal, got %v", snap.Signals)
	}
	if snap.Score != 0.8 {
		t.Fatalf("expected score=0.8, got %v", snap.Score)
	}
}

func TestEngine_ToolErrorSignal(t *testing.T) {
	e := attention.New(attention.DefaultWeights())
	snap := e.OnEvent("s1", "tool.error")
	if !snap.Signals["tool_error"] {
		t.Fatalf("expected tool_error signal, got %v", snap.Signals)
	}
}

func TestEngine_ToolPreClearsSignals(t *testing.T) {
	e := attention.New(attention.DefaultWeights())
	e.OnEvent("s1", "session.idle")
	e.OnEvent("s1", "notification.needs_attention")
	snap := e.OnEvent("s1", "tool.pre")
	if len(snap.Signals) != 0 {
		t.Fatalf("expected empty signals after tool.pre, got %v", snap.Signals)
	}
	if snap.Score != 0 {
		t.Fatalf("expected score=0, got %v", snap.Score)
	}
}

func TestEngine_SessionStartClearsSignals(t *testing.T) {
	e := attention.New(attention.DefaultWeights())
	e.OnEvent("s1", "session.idle")
	snap := e.OnEvent("s1", "session.start")
	if len(snap.Signals) != 0 {
		t.Fatalf("expected empty signals after session.start, got %v", snap.Signals)
	}
}

func TestEngine_SignalsAreAdditive(t *testing.T) {
	e := attention.New(attention.DefaultWeights())
	e.OnEvent("s1", "session.idle")                  // +1.0
	e.OnEvent("s1", "notification.needs_attention")  // +0.8
	snap := e.OnEvent("s1", "tool.error")             // +0.5
	// paused + notification + tool_error = 1.0 + 0.8 + 0.5 = 2.3
	if snap.Score < 2.29 || snap.Score > 2.31 {
		t.Fatalf("expected additive score ~2.3, got %v (signals=%v)", snap.Score, snap.Signals)
	}
}

func TestEngine_StalenessRamp(t *testing.T) {
	now := time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC)
	e := attention.New(attention.DefaultWeights())
	e.SetNow(func() time.Time { return now })

	// Fire any event to set lastEventAt = now.
	e.OnEvent("s1", "tool.pre")

	// Advance 2 minutes — under StalenessStart (5min). No staleness.
	e.SetNow(func() time.Time { return now.Add(2 * time.Minute) })
	snap := e.Recompute("s1")
	if snap.Signals["staleness"] {
		t.Fatalf("expected no staleness at 2min, got %v", snap.Signals)
	}

	// Advance to 10 minutes — halfway through ramp (5–15min). Expect ~0.15.
	e.SetNow(func() time.Time { return now.Add(10 * time.Minute) })
	snap = e.Recompute("s1")
	if !snap.Signals["staleness"] {
		t.Fatalf("expected staleness at 10min")
	}
	if snap.Score < 0.14 || snap.Score > 0.16 {
		t.Fatalf("expected staleness ~0.15 at 10min, got %v", snap.Score)
	}

	// Advance to 20 minutes — past StalenessFull. Expect cap 0.3.
	e.SetNow(func() time.Time { return now.Add(20 * time.Minute) })
	snap = e.Recompute("s1")
	if snap.Score < 0.29 || snap.Score > 0.31 {
		t.Fatalf("expected staleness cap 0.3 at 20min, got %v", snap.Score)
	}
}

func TestEngine_Forget(t *testing.T) {
	e := attention.New(attention.DefaultWeights())
	e.OnEvent("s1", "session.idle")
	e.Forget("s1")
	snap := e.Recompute("s1")
	if snap.Score != 0 || len(snap.Signals) != 0 {
		t.Fatalf("expected zero state after Forget, got %v", snap)
	}
}

func TestSnapshot_JSON(t *testing.T) {
	snap := attention.Snapshot{
		Score: 1.8,
		Signals: map[string]bool{
			"paused":       true,
			"notification": true,
			"ignored":      false,
		},
	}
	got := snap.JSON()
	if !strings.Contains(got, "paused") || !strings.Contains(got, "notification") {
		t.Fatalf("expected JSON to contain active signals, got %q", got)
	}
	if strings.Contains(got, "ignored") {
		t.Fatalf("JSON should not include inactive signals: %q", got)
	}
}
