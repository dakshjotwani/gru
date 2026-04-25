package tailer_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/publisher"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/tailer"
)

// TestEndToEnd_fakeClaude simulates a Claude session by appending
// timed JSONL lines to a transcript file. Asserts the publisher
// fans new events out to a subscriber and the sessions row converges
// on the deterministically-folded final status.
//
// This is the "smoke test in CI" the spec asks for.
func TestEndToEnd_fakeClaude(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	if _, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "p", Name: "p", Adapter: "host", Runtime: "claude-code",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-e2e", ProjectID: "p", Runtime: "claude-code", Status: "starting",
	}); err != nil {
		t.Fatal(err)
	}

	pub := publisher.NewPublisher(s)
	pub.SetBufferSize(64)
	pubCtx, pubCancel := context.WithCancel(ctx)
	defer pubCancel()
	go pub.Run(pubCtx)

	dir := t.TempDir()
	transcript := filepath.Join(dir, "claude.jsonl")
	if err := os.WriteFile(transcript, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	// Spawn the tailer wired to the publisher so commits trigger
	// fan-out.
	tlCtx, tlCancel := context.WithCancel(ctx)
	defer tlCancel()
	tl := tailer.New(tailer.Config{
		SessionID: "sess-e2e", ProjectID: "p", Runtime: "claude-code",
		TranscriptPath: transcript,
		NotifyPath:     filepath.Join(dir, "notify.jsonl"),
		SupervisorPath: filepath.Join(dir, "sup.jsonl"),
		Store:          s,
		Notifier:       pub,
		PollInterval:   25 * time.Millisecond,
	})
	tlDone := make(chan struct{})
	go func() {
		_ = tl.Run(tlCtx)
		close(tlDone)
	}()
	defer func() {
		tlCancel()
		<-tlDone
	}()

	// Subscribe a "client" — analogous to the React app over gRPC.
	sub, _, err := pub.Subscribe("e2e-client", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Unsubscribe("e2e-client")

	// Drive a fake Claude that emits a tool_use, then resolves it,
	// then ends the turn.
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Errorf("open transcript: %v", err)
			return
		}
		defer f.Close()
		lines := []string{
			`{"type":"permission-mode","permissionMode":"default"}`,
			`{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1"}]}}`,
			`{"type":"user","timestamp":"2026-04-25T00:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1"}]}}`,
			`{"type":"assistant","timestamp":"2026-04-25T00:00:02Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}`,
		}
		for _, ln := range lines {
			if _, err := fmt.Fprintln(f, ln); err != nil {
				t.Errorf("write transcript: %v", err)
				return
			}
			_ = f.Sync()
			time.Sleep(40 * time.Millisecond)
		}
	}()

	// Drain incoming events until we see status flip to idle in the
	// sessions row.
	deadline := time.Now().Add(3 * time.Second)
	gotIdle := false
	gotEvents := 0
loop:
	for time.Now().Before(deadline) {
		select {
		case <-sub.Events():
			gotEvents++
			row, err := s.Queries().GetSession(ctx, "sess-e2e")
			if err != nil {
				continue
			}
			if row.Status == "idle" {
				gotIdle = true
				break loop
			}
		case <-time.After(time.Until(deadline)):
			break loop
		}
	}
	wg.Wait()
	if !gotIdle {
		t.Fatalf("session did not reach idle within deadline (received %d events)", gotEvents)
	}
	if gotEvents == 0 {
		t.Fatalf("publisher delivered zero events")
	}
	row, _ := s.Queries().GetSession(ctx, "sess-e2e")
	if row.PermissionMode != "default" {
		t.Errorf("permission_mode = %q, want default", row.PermissionMode)
	}
}
