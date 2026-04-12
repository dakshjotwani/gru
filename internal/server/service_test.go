package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
)

func newTestServer(t *testing.T) (gruv1connect.GruServiceClient, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	pub := ingestion.NewPublisher()
	svc := server.NewService(s, pub)
	mux := http.NewServeMux()
	mux.Handle(gruv1connect.NewGruServiceHandler(svc))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	client := gruv1connect.NewGruServiceClient(ts.Client(), ts.URL)
	return client, s
}

func TestListSessions_empty(t *testing.T) {
	client, _ := newTestServer(t)
	resp, err := client.ListSessions(context.Background(),
		connect.NewRequest(&gruv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.Msg.Sessions) != 0 {
		t.Errorf("sessions = %d, want 0", len(resp.Msg.Sessions))
	}
}

func TestListSessions_afterInsert(t *testing.T) {
	client, s := newTestServer(t)
	ctx := context.Background()

	_, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "p1", Name: "proj", Path: "/tmp/proj", Runtime: "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "s1", ProjectID: "p1", Runtime: "claude-code", Status: "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.ListSessions(ctx, connect.NewRequest(&gruv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(resp.Msg.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(resp.Msg.Sessions))
	}
	if resp.Msg.Sessions[0].Id != "s1" {
		t.Errorf("session id = %q, want %q", resp.Msg.Sessions[0].Id, "s1")
	}
}

func TestGetSession_notFound(t *testing.T) {
	client, _ := newTestServer(t)
	_, err := client.GetSession(context.Background(),
		connect.NewRequest(&gruv1.GetSessionRequest{Id: "nonexistent"}))
	if err == nil {
		t.Fatal("expected error for nonexistent session, got nil")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("error code = %v, want NotFound", connect.CodeOf(err))
	}
}

func TestListProjects(t *testing.T) {
	client, s := newTestServer(t)
	ctx := context.Background()

	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "p1", Name: "alpha", Path: "/a", Runtime: "claude-code",
	})
	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "p2", Name: "beta", Path: "/b", Runtime: "claude-code",
	})

	resp, err := client.ListProjects(ctx, connect.NewRequest(&gruv1.ListProjectsRequest{}))
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(resp.Msg.Projects) != 2 {
		t.Errorf("projects = %d, want 2", len(resp.Msg.Projects))
	}
}
