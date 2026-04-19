//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// stubNormalizer is a test-only EventNormalizer for the "test-runtime".
type stubNormalizer struct{}

func (s *stubNormalizer) RuntimeID() string { return "test-runtime" }
func (s *stubNormalizer) Normalize(_ context.Context, raw json.RawMessage) (*adapter.GruEvent, error) {
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &adapter.GruEvent{
		ID:        "evt-integration-1",
		SessionID: m["session_id"],
		ProjectID: m["project_id"],
		Runtime:   "test-runtime",
		Type:      adapter.EventSessionStart,
		Timestamp: time.Now().UTC(),
		Payload:   raw,
	}, nil
}

// startTestServer starts an in-process server using httptest and returns its URL.
// It also returns the store so the caller can seed data.
func startTestServer(t *testing.T) (url string, s *store.Store) {
	t.Helper()

	var err error
	s, err = store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	pub := ingestion.NewPublisher()
	reg := adapter.NewRegistry()
	reg.Register(&stubNormalizer{})

	svc := server.NewService(s, pub)
	ingestionHandler := ingestion.NewHandler(s, reg, pub)

	mux := http.NewServeMux()
	grpcPath, grpcHandler := gruv1connect.NewGruServiceHandler(svc)
	mux.Handle(grpcPath, grpcHandler)
	mux.Handle("POST /events", ingestionHandler)

	ts := httptest.NewUnstartedServer(h2c.NewHandler(mux, &http2.Server{}))
	ts.Start()
	t.Cleanup(ts.Close)

	return ts.URL, s
}

func TestIntegration_PostEventAndListSession(t *testing.T) {
	url, s := startTestServer(t)
	ctx := context.Background()

	// Pre-seed project + session — sessions must pre-exist before any hook fires.
	// In production, the launcher creates the session before starting the tmux window.
	_, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID:      "proj-int",
		Name:    "integration-project",
		Adapter: "host",
		Runtime: "test-runtime",
	})
	if err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	_, err = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID:        "sess-int",
		ProjectID: "proj-int",
		Runtime:   "test-runtime",
		Status:    "starting",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// POST an event to the ingestion endpoint.
	body := `{"session_id":"sess-int","project_id":"proj-int"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/events",
		bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Runtime", "test-runtime")
	req.Header.Set("X-Gru-Session-ID", "sess-int")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /events: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /events status = %d, want 202", resp.StatusCode)
	}

	// Verify via gRPC ListSessions.
	client := gruv1connect.NewGruServiceClient(http.DefaultClient, url)
	listResp, err := client.ListSessions(ctx,
		connect.NewRequest(&gruv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(listResp.Msg.Sessions) != 1 {
		t.Errorf("sessions = %d, want 1", len(listResp.Msg.Sessions))
	}
	if listResp.Msg.Sessions[0].Id != "sess-int" {
		t.Errorf("session id = %q, want %q", listResp.Msg.Sessions[0].Id, "sess-int")
	}
}
