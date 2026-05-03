package ingest

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestAppend_RoundTrip verifies the basic append → read-back path.
func TestAppend_RoundTrip(t *testing.T) {
	home := t.TempDir()
	ev := Event{Type: TypeTurnStarted, Trigger: "user_prompt"}
	if err := Append(home, "sess-1", ev); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got := readLog(t, home, "sess-1")
	if len(got) != 1 {
		t.Fatalf("got %d lines, want 1", len(got))
	}
	if got[0].Version != SchemaVersion {
		t.Errorf("version = %d, want %d", got[0].Version, SchemaVersion)
	}
	if got[0].Type != TypeTurnStarted {
		t.Errorf("type = %q, want %q", got[0].Type, TypeTurnStarted)
	}
	if got[0].Trigger != "user_prompt" {
		t.Errorf("trigger = %q", got[0].Trigger)
	}
	if got[0].Ts == "" {
		t.Errorf("ts populated automatically expected")
	}
}

// TestAppend_RejectsEmptySessionID prevents accidental writes to a
// log file with no session attribution.
func TestAppend_RejectsEmptySessionID(t *testing.T) {
	if err := Append(t.TempDir(), "", Event{Type: TypeTurnStarted}); err == nil {
		t.Fatal("expected error for empty session id")
	}
}

// TestAppend_ConcurrentSafe checks that interleaved appends from
// many goroutines produce well-formed JSON lines (no torn writes).
// On macOS PIPE_BUF is 512; we keep events small so the kernel
// guarantees atomic appends.
func TestAppend_ConcurrentSafe(t *testing.T) {
	home := t.TempDir()
	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := Event{Type: TypeToolCompleted, Tool: "Bash", Ok: boolPtr(i%2 == 0)}
			_ = Append(home, "sess-cc", ev)
		}(i)
	}
	wg.Wait()
	got := readLog(t, home, "sess-cc")
	if len(got) != N {
		t.Fatalf("got %d lines, want %d", len(got), N)
	}
	for i, ev := range got {
		if ev.Type != TypeToolCompleted {
			t.Fatalf("line %d: type = %q", i, ev.Type)
		}
	}
}

// TestTranslateClaudeHook_Notification covers the most load-bearing
// translation (idle_prompt → needs_attention).
func TestTranslateClaudeHook_Notification(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "myproject")
	mustWrite(t, filepath.Join(cwd, ".gru", "session-id"), "gru-sid-1")

	payload := `{
        "hook_event_name":"Notification",
        "session_id":"gru-sid-1",
        "cwd":"` + cwd + `",
        "notification_type":"idle_prompt",
        "message":"Claude is waiting for your input"
    }`
	sid, ev, err := TranslateClaudeHook([]byte(payload))
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if sid != "gru-sid-1" {
		t.Errorf("sid = %q", sid)
	}
	if ev.Type != TypeAttentionRequested {
		t.Errorf("type = %q", ev.Type)
	}
	if ev.Reason != "idle_prompt" {
		t.Errorf("reason = %q", ev.Reason)
	}
}

// TestTranslateClaudeHook_SiblingGuard rejects payloads from a
// non-matching Claude process — the bug we fixed earlier today.
func TestTranslateClaudeHook_SiblingGuard(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "myproject")
	mustWrite(t, filepath.Join(cwd, ".gru", "session-id"), "gru-sid-1")

	payload := `{
        "hook_event_name":"Notification",
        "session_id":"some-other-claude-uuid",
        "cwd":"` + cwd + `",
        "notification_type":"idle_prompt"
    }`
	sid, _, err := TranslateClaudeHook([]byte(payload))
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if sid != "" {
		t.Errorf("expected empty sid (rejected), got %q", sid)
	}
}

// TestTranslateClaudeHook_PostToolUse covers the running→running
// signal — most-frequent hook.
func TestTranslateClaudeHook_PostToolUse(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "myproject")
	mustWrite(t, filepath.Join(cwd, ".gru", "session-id"), "gru-sid-2")

	payload := `{
        "hook_event_name":"PostToolUse",
        "session_id":"gru-sid-2",
        "cwd":"` + cwd + `",
        "tool_name":"Bash"
    }`
	sid, ev, err := TranslateClaudeHook([]byte(payload))
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if sid != "gru-sid-2" || ev.Type != TypeToolCompleted || ev.Tool != "Bash" {
		t.Fatalf("unexpected: sid=%q ev=%+v", sid, ev)
	}
	if ev.Ok == nil || *ev.Ok != true {
		t.Errorf("ok = %v, want pointer to true", ev.Ok)
	}
}

// TestTranslateClaudeHook_UnknownPassThrough preserves Claude's raw
// payload for events we don't fold yet.
func TestTranslateClaudeHook_UnknownPassThrough(t *testing.T) {
	dir := t.TempDir()
	cwd := filepath.Join(dir, "myproject")
	mustWrite(t, filepath.Join(cwd, ".gru", "session-id"), "gru-sid-3")

	payload := `{"hook_event_name":"PreToolUse","session_id":"gru-sid-3","cwd":"` + cwd + `","tool_name":"Read"}`
	sid, ev, err := TranslateClaudeHook([]byte(payload))
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if sid == "" {
		t.Fatal("expected resolved sid")
	}
	if ev.Type != TypeUnknown || ev.ClaudeEvent != "PreToolUse" {
		t.Fatalf("ev = %+v", ev)
	}
	// Raw is preserved.
	if len(ev.Raw) == 0 {
		t.Error("raw payload not preserved")
	}
}

// TestTranslateClaudeHook_NoCwdFile yields empty sid (skip).
func TestTranslateClaudeHook_NoCwdFile(t *testing.T) {
	payload := `{"hook_event_name":"Stop","session_id":"x","cwd":"/nonexistent/dir"}`
	sid, _, err := TranslateClaudeHook([]byte(payload))
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if sid != "" {
		t.Errorf("expected skip; got sid=%q", sid)
	}
}

// ── helpers ───────────────────────────────────────────────────────────

func readLog(t *testing.T, home, sid string) []Event {
	t.Helper()
	f, err := os.Open(LogPath(home, sid))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("parse line: %v (raw=%s)", err, sc.Text())
		}
		out = append(out, ev)
	}
	return out
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
