package tailer_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/state"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/tailer"
)

// countingNotifier records every Notify call so tests can assert that
// the publisher would have been signalled.
type countingNotifier struct {
	mu    sync.Mutex
	calls []string
}

func (c *countingNotifier) Notify(sid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, sid)
}

func (c *countingNotifier) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.calls))
	copy(out, c.calls)
	return out
}

// setup boots an in-memory store, creates a project + session row,
// and returns everything needed to drive a tailer.
func setup(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	if _, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-test", Name: "test", Adapter: "host", Runtime: "claude-code",
	}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	sid := "sess-" + t.Name()
	if _, err := s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: sid, ProjectID: "proj-test", Runtime: "claude-code", Status: "starting",
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return s, "proj-test", sid
}

// writeLines appends each line + '\n' to path. Used to simulate Claude
// writing to its transcript.
func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	for _, ln := range lines {
		if _, err := f.WriteString(ln + "\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	_ = f.Sync()
}

// runTailerUntil starts a tailer and waits up to 2 s for the predicate
// to hold. Used to side-step having to coordinate fsnotify events with
// the test.
func runTailerUntil(t *testing.T, cfg tailer.Config, predicate func(*tailer.Tailer) bool) (*tailer.Tailer, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	tl := tailer.New(cfg)
	go func() {
		if err := tl.Run(ctx); err != nil {
			t.Logf("tailer.Run err: %v", err)
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if predicate(tl) {
			return tl, cancel
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatalf("tailer did not reach predicate within deadline; final state = %+v", tl.State())
	return nil, nil
}

// ── basic happy path ─────────────────────────────────────────────────

func TestTailer_appendedLineUpdatesState(t *testing.T) {
	s, pid, sid := setup(t)
	dir := t.TempDir()
	transcript := filepath.Join(dir, "t.jsonl")
	notify := filepath.Join(dir, "notify.jsonl")
	notifier := &countingNotifier{}

	cfg := tailer.Config{
		SessionID: sid, ProjectID: pid, Runtime: "claude-code",
		TranscriptPath: transcript, NotifyPath: notify,
		Store: s, Notifier: notifier,
		PollInterval: 50 * time.Millisecond,
	}

	// Empty file at startup.
	if err := os.WriteFile(transcript, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	tl, cancel := runTailerUntil(t, cfg, func(_ *tailer.Tailer) bool {
		// Tailer ran initial wipe; that's enough to know it's alive.
		row, err := s.Queries().GetSession(context.Background(), sid)
		return err == nil && row.Status == "starting"
	})
	defer cancel()

	// Append a clean end_turn line; expect status to flip to idle.
	writeLines(t, transcript,
		`{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}`,
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		row, err := s.Queries().GetSession(context.Background(), sid)
		if err == nil && row.Status == "idle" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	row, err := s.Queries().GetSession(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "idle" {
		t.Fatalf("session status = %q, want idle (state=%+v)", row.Status, tl.State())
	}
	// The notifier must have been signalled at least once.
	if len(notifier.Calls()) == 0 {
		t.Fatal("notifier was not called after commit")
	}
}

// ── partial-line buffering ───────────────────────────────────────────

func TestTailer_partialTrailingLineBuffered(t *testing.T) {
	s, pid, sid := setup(t)
	dir := t.TempDir()
	transcript := filepath.Join(dir, "t.jsonl")
	notify := filepath.Join(dir, "notify.jsonl")
	notifier := &countingNotifier{}

	cfg := tailer.Config{
		SessionID: sid, ProjectID: pid, Runtime: "claude-code",
		TranscriptPath: transcript, NotifyPath: notify,
		Store: s, Notifier: notifier,
		PollInterval: 25 * time.Millisecond,
	}
	if err := os.WriteFile(transcript, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	_, cancel := runTailerUntil(t, cfg, func(*tailer.Tailer) bool {
		_, err := s.Queries().GetSession(context.Background(), sid)
		return err == nil
	})
	defer cancel()

	// Write the first half of an idle line WITHOUT a trailing newline.
	// The tailer should treat this as a partial line and buffer it.
	if err := os.WriteFile(transcript,
		[]byte(`{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}`+"\n"+
			`{"type":"assistant","timestamp":"2026-04-25T00:00:01Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"a"}]}}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	// First line should be applied; partial second line buffered.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		row, _ := s.Queries().GetSession(context.Background(), sid)
		if row.Status == "idle" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	row, _ := s.Queries().GetSession(context.Background(), sid)
	if row.Status != "idle" {
		t.Fatalf("after first complete line, status = %q, want idle", row.Status)
	}
	// Now finish the partial line by appending the trailing newline.
	writeLines(t, transcript, "")
	for time.Now().Before(deadline) {
		row, _ := s.Queries().GetSession(context.Background(), sid)
		if row.Status == "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	row, _ = s.Queries().GetSession(context.Background(), sid)
	if row.Status != "running" {
		t.Fatalf("after completing partial line, status = %q, want running", row.Status)
	}
}

// ── replay-from-zero correctness (the §3.2 property) ─────────────────

func TestTailer_replayFromZeroProducesSameSessionRow(t *testing.T) {
	s, pid, sid := setup(t)
	dir := t.TempDir()
	transcript := filepath.Join(dir, "t.jsonl")
	notify := filepath.Join(dir, "notify.jsonl")

	// Build a multi-step transcript on disk first.
	if err := os.WriteFile(transcript, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	writeLines(t, transcript,
		`{"type":"permission-mode","permissionMode":"plan"}`,
		`{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"a"}]}}`,
		`{"type":"user","timestamp":"2026-04-25T00:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"a"}]}}`,
		`{"type":"assistant","timestamp":"2026-04-25T00:00:02Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}`,
	)

	makeCfg := func() tailer.Config {
		return tailer.Config{
			SessionID: sid, ProjectID: pid, Runtime: "claude-code",
			TranscriptPath: transcript, NotifyPath: notify,
			Store: s, Notifier: &countingNotifier{},
			PollInterval: 25 * time.Millisecond,
		}
	}

	// First run: tailer wipes events, replays, derives idle.
	_, cancel := runTailerUntil(t, makeCfg(), func(*tailer.Tailer) bool {
		row, _ := s.Queries().GetSession(context.Background(), sid)
		return row.Status == "idle"
	})
	cancel()

	first, err := s.Queries().GetSession(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	firstEvents, err := s.Queries().ListEventsBySession(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}

	// Second run: identical input file, fresh tailer. The events
	// projection gets wiped and rebuilt — the resulting session row
	// MUST be identical (status, permission_mode, claude_stop_reason,
	// last_event_at). Event row count must also match.
	_, cancel = runTailerUntil(t, makeCfg(), func(*tailer.Tailer) bool {
		row, _ := s.Queries().GetSession(context.Background(), sid)
		return row.Status == "idle" && row.PermissionMode == "plan"
	})
	cancel()

	second, err := s.Queries().GetSession(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	secondEvents, err := s.Queries().ListEventsBySession(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}

	if first.Status != second.Status ||
		first.PermissionMode != second.PermissionMode ||
		first.ClaudeStopReason != second.ClaudeStopReason {
		t.Fatalf("replay produced different row:\n  first  = %+v\n  second = %+v", first, second)
	}
	if len(firstEvents) != len(secondEvents) {
		t.Fatalf("replay produced different event count: first=%d second=%d", len(firstEvents), len(secondEvents))
	}
}

// ── notification path: permission_prompt → needs_attention ───────────

func TestTailer_notificationFlipsToNeedsAttention(t *testing.T) {
	s, pid, sid := setup(t)
	dir := t.TempDir()
	transcript := filepath.Join(dir, "t.jsonl")
	notify := filepath.Join(dir, "notify.jsonl")
	notifier := &countingNotifier{}

	cfg := tailer.Config{
		SessionID: sid, ProjectID: pid, Runtime: "claude-code",
		TranscriptPath: transcript, NotifyPath: notify,
		Store: s, Notifier: notifier,
		PollInterval: 25 * time.Millisecond,
	}
	if err := os.WriteFile(transcript, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notify, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	_, cancel := runTailerUntil(t, cfg, func(*tailer.Tailer) bool {
		_, err := s.Queries().GetSession(context.Background(), sid)
		return err == nil
	})
	defer cancel()

	// Append a permission_prompt line to the notify file. Expect:
	//  - sessions.status = needs_attention
	//  - a session.transition event row was emitted
	writeLines(t, notify, `{"hook_event_name":"Notification","notification_type":"permission_prompt"}`)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		row, _ := s.Queries().GetSession(context.Background(), sid)
		if row.Status == "needs_attention" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	row, _ := s.Queries().GetSession(context.Background(), sid)
	if row.Status != "needs_attention" {
		t.Fatalf("status = %q, want needs_attention", row.Status)
	}
	events, _ := s.Queries().ListEventsBySession(context.Background(), sid)
	hasTransition := false
	for _, e := range events {
		if e.Type == "session.transition" {
			hasTransition = true
		}
	}
	if !hasTransition {
		t.Fatalf("expected a session.transition event row; got: %v", eventTypes(events))
	}
}

// ── polling fallback (fsnotify disabled) ─────────────────────────────

func TestTailer_pollingFallbackWorks(t *testing.T) {
	s, pid, sid := setup(t)
	dir := t.TempDir()
	transcript := filepath.Join(dir, "t.jsonl")
	notify := filepath.Join(dir, "notify.jsonl")
	notifier := &countingNotifier{}

	use := false
	cfg := tailer.Config{
		SessionID: sid, ProjectID: pid, Runtime: "claude-code",
		TranscriptPath: transcript, NotifyPath: notify,
		Store: s, Notifier: notifier,
		PollInterval: 25 * time.Millisecond,
		UseFsnotify:  &use,
	}
	if err := os.WriteFile(transcript, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, cancel := runTailerUntil(t, cfg, func(*tailer.Tailer) bool {
		_, err := s.Queries().GetSession(context.Background(), sid)
		return err == nil
	})
	defer cancel()

	writeLines(t, transcript,
		`{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}`,
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		row, _ := s.Queries().GetSession(context.Background(), sid)
		if row.Status == "idle" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	row, _ := s.Queries().GetSession(context.Background(), sid)
	if row.Status != "idle" {
		t.Fatalf("polling-only tailer didn't update status; got %q", row.Status)
	}
}

// ── 1000-line stress: final state matches deterministic fold ─────────

func TestTailer_thousandLinesMatchesPureFold(t *testing.T) {
	s, pid, sid := setup(t)
	dir := t.TempDir()
	transcript := filepath.Join(dir, "t.jsonl")
	notify := filepath.Join(dir, "notify.jsonl")
	notifier := &countingNotifier{}

	// Build a synthetic transcript and write it all at once.
	var lines []string
	for i := 0; i < 1000; i++ {
		switch i % 4 {
		case 0:
			lines = append(lines,
				fmt.Sprintf(`{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t%d"}]}}`, i))
		case 1:
			lines = append(lines,
				fmt.Sprintf(`{"type":"user","timestamp":"2026-04-25T00:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"t%d"}]}}`, i-1))
		case 2:
			lines = append(lines, `{"type":"file-history-snapshot"}`)
		case 3:
			lines = append(lines, `{"type":"assistant","timestamp":"2026-04-25T00:00:02Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}`)
		}
	}

	if err := os.WriteFile(transcript, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	writeLines(t, transcript, lines...)

	cfg := tailer.Config{
		SessionID: sid, ProjectID: pid, Runtime: "claude-code",
		TranscriptPath: transcript, NotifyPath: notify,
		Store: s, Notifier: notifier,
		PollInterval: 25 * time.Millisecond,
	}
	_, cancel := runTailerUntil(t, cfg, func(*tailer.Tailer) bool {
		row, _ := s.Queries().GetSession(context.Background(), sid)
		return row.Status == "idle" || row.Status == "running"
	})
	defer cancel()

	// Compute the expected final state via the pure fold for comparison.
	expectedState := state.Initial()
	for _, ln := range lines {
		var p *state.Projected
		expectedState, p = state.Derive(expectedState, state.SourceTranscript, []byte(ln))
		_ = p
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, _ := s.Queries().GetSession(context.Background(), sid)
		if string(expectedState.Status) == row.Status {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	row, _ := s.Queries().GetSession(context.Background(), sid)
	if string(expectedState.Status) != row.Status {
		t.Fatalf("after 1000 lines, status = %q, want %q", row.Status, expectedState.Status)
	}
}

func eventTypes(rows []store.Event) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Type)
	}
	return out
}
