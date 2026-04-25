//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/publisher"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/tailer"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// startTestServer boots an in-process Gru server (gRPC only — the
// /events HTTP endpoint is gone in rev 2) and a publisher. Returns
// the URL and store so the caller can seed data and assert.
func startTestServer(t *testing.T, ctx context.Context) (string, *store.Store, *publisher.Publisher, *tailer.Manager) {
	t.Helper()

	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	pub := publisher.NewPublisher(s)
	go pub.Run(ctx)

	homeDir := t.TempDir()
	mgr := tailer.NewManager(s, pub, homeDir)

	svc := server.NewService(s, pub)
	svc.SetTailerManager(mgr)

	mux := http.NewServeMux()
	grpcPath, grpcHandler := gruv1connect.NewGruServiceHandler(svc)
	mux.Handle(grpcPath, grpcHandler)

	ts := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	ts.Start()
	t.Cleanup(ts.Close)

	return ts.URL, s, pub, mgr
}

// TestIntegration_TailerEndToEnd walks through the rev-2 pipeline:
//   - seed a session
//   - point its tailer at a JSONL transcript on disk
//   - append an end_turn assistant line
//   - assert ListSessions reflects status=idle
//
// This exercises every layer between the producer (file writer) and
// the consumer (gRPC) in one shot.
func TestIntegration_TailerEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	url, s, _, mgr := startTestServer(t, ctx)

	// Pre-seed project + session.
	if _, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-int", Name: "integration", Adapter: "host", Runtime: "claude-code",
	}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if _, err := s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-int", ProjectID: "proj-int", Runtime: "claude-code", Status: "starting",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Spin up a tailer for the session manually (in production this
	// happens in LaunchSession; the integration test bypasses launch
	// because it doesn't have a real tmux pane to attach to).
	dir := t.TempDir()
	transcript := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(transcript, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	mgr.AddSession(ctx, "sess-int", "proj-int", "claude-code", transcript)
	t.Cleanup(mgr.StopAll)

	// Append an end_turn line. The tailer should flip status to idle
	// and the events projection should hold a row.
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"assistant","timestamp":"2026-04-25T00:00:00Z","message":{"stop_reason":"end_turn","content":[{"type":"text"}]}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Poll ListSessions until status=idle or deadline.
	client := gruv1connect.NewGruServiceClient(http.DefaultClient, url)
	deadline := time.Now().Add(3 * time.Second)
	var got *gruv1.Session
	for time.Now().Before(deadline) {
		resp, err := client.ListSessions(ctx, connect.NewRequest(&gruv1.ListSessionsRequest{}))
		if err == nil && len(resp.Msg.Sessions) == 1 {
			if resp.Msg.Sessions[0].Status == gruv1.SessionStatus_SESSION_STATUS_IDLE {
				got = resp.Msg.Sessions[0]
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got == nil {
		t.Fatalf("session did not reach idle within deadline")
	}
	if got.Id != "sess-int" {
		t.Errorf("session id = %q, want %q", got.Id, "sess-int")
	}
	if got.ClaudeStopReason != "end_turn" {
		t.Errorf("claude_stop_reason = %q, want end_turn", got.ClaudeStopReason)
	}
}
