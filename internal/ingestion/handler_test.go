package ingestion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dakshjotwani/gru/internal/adapter"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
)

// stubNormalizer is a minimal EventNormalizer for testing.
type stubNormalizer struct{}

func (s *stubNormalizer) RuntimeID() string { return "test-runtime" }
func (s *stubNormalizer) Normalize(_ context.Context, raw json.RawMessage) (*adapter.GruEvent, error) {
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &adapter.GruEvent{
		ID:        "evt-1",
		SessionID: m["session_id"],
		ProjectID: m["project_id"],
		Runtime:   "test-runtime",
		Type:      adapter.EventSessionStart,
		Payload:   raw,
	}, nil
}

func setup(t *testing.T) (*store.Store, *adapter.Registry) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	reg := adapter.NewRegistry()
	reg.Register(&stubNormalizer{})
	return s, reg
}

func TestHandler_rejectsMissingSessionID(t *testing.T) {
	s, reg := setup(t)
	pub := ingestion.NewPublisher()
	h := ingestion.NewHandler(s, reg, pub)

	body := `{"hook_event_name":"PreToolUse"}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Runtime", "test-runtime")
	// No X-Gru-Session-ID header
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandler_rejectsUnknownSession(t *testing.T) {
	s, reg := setup(t)
	pub := ingestion.NewPublisher()
	h := ingestion.NewHandler(s, reg, pub)

	body := `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Runtime", "test-runtime")
	req.Header.Set("X-Gru-Session-ID", "nonexistent-session")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandler_storesEvent(t *testing.T) {
	s, reg := setup(t)
	pub := ingestion.NewPublisher()
	h := ingestion.NewHandler(s, reg, pub)

	// Session pre-exists (created by launcher before tmux window starts).
	ctx := context.Background()
	_, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-1", Name: "test", Adapter: "host", Runtime: "test-runtime",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-1", ProjectID: "proj-1", Runtime: "test-runtime", Status: "starting",
	})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"session_id":"sess-1","project_id":"proj-1"}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Runtime", "test-runtime")
	req.Header.Set("X-Gru-Session-ID", "sess-1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusAccepted, rr.Body.String())
	}

	events, err := s.Queries().ListEventsBySession(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("events in DB = %d, want 1", len(events))
	}
}

func TestHandler_publishesToSubscribers(t *testing.T) {
	s, reg := setup(t)
	pub := ingestion.NewPublisher()

	sub := make(chan *gruv1.SessionEvent, 1)
	pub.Subscribe("test-sub", sub)
	defer pub.Unsubscribe("test-sub")

	h := ingestion.NewHandler(s, reg, pub)

	ctx := context.Background()
	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-1", Name: "test", Adapter: "host", Runtime: "test-runtime",
	})
	_, _ = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-1", ProjectID: "proj-1", Runtime: "test-runtime", Status: "starting",
	})

	body := `{"session_id":"sess-1","project_id":"proj-1"}`
	req := httptest.NewRequest(http.MethodPost, "/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gru-Runtime", "test-runtime")
	req.Header.Set("X-Gru-Session-ID", "sess-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	select {
	case evt := <-sub:
		if evt.SessionId != "sess-1" {
			t.Errorf("published event session_id = %q, want %q", evt.SessionId, "sess-1")
		}
	default:
		t.Error("no event published to subscriber")
	}
}
