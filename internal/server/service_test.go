package server_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/controller"
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

// newTestService returns a *Service and the underlying *store.Store for direct method testing.
func newTestService(t *testing.T) (*server.Service, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	pub := ingestion.NewPublisher()
	svc := server.NewService(s, pub)
	return svc, s
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

func TestService_LaunchSession(t *testing.T) {
	svc, s := newTestService(t)

	reg := controller.NewRegistry()
	launched := make(chan controller.LaunchOptions, 1)
	reg.Register(&fakeSessionController{
		runtimeID: "claude-code",
		launchFn: func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
			launched <- opts
			done := make(chan struct{})
			close(done)
			return &controller.SessionHandle{
				SessionID:   opts.SessionID,
				TmuxSession: "gru-testproject",
				TmuxWindow:  "feat-dev·abcd1234",
			}, nil
		},
	})
	svc.SetControllerRegistry(reg)

	projectDir := t.TempDir()
	req := connect.NewRequest(&gruv1.LaunchSessionRequest{
		ProjectDir: projectDir,
		Prompt:     "write tests",
		Profile:    "default",
		Name:       "test-session",
	})

	resp, err := svc.LaunchSession(context.Background(), req)
	if err != nil {
		t.Fatalf("LaunchSession: unexpected error: %v", err)
	}
	if resp.Msg.Session == nil {
		t.Fatal("expected Session in response, got nil")
	}
	sess := resp.Msg.Session
	if sess.Id == "" {
		t.Error("session ID is empty")
	}
	if sess.Status != gruv1.SessionStatus_SESSION_STATUS_STARTING {
		t.Errorf("Status = %v, want STARTING", sess.Status)
	}
	if sess.Runtime != "claude-code" {
		t.Errorf("Runtime = %q, want claude-code", sess.Runtime)
	}
	if sess.TmuxSession != "gru-testproject" {
		t.Errorf("TmuxSession = %q, want gru-testproject", sess.TmuxSession)
	}
	if sess.TmuxWindow != "feat-dev·abcd1234" {
		t.Errorf("TmuxWindow = %q, want feat-dev·abcd1234", sess.TmuxWindow)
	}

	select {
	case opts := <-launched:
		if opts.Prompt != "write tests" {
			t.Errorf("Prompt = %q, want %q", opts.Prompt, "write tests")
		}
		if opts.SessionID == "" {
			t.Error("SessionID was not set in LaunchOptions")
		}
	default:
		t.Error("controller.Launch was not called")
	}

	stored, err := s.Queries().GetSession(context.Background(), sess.Id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if stored.TmuxSession == nil || *stored.TmuxSession != "gru-testproject" {
		t.Errorf("stored TmuxSession = %v, want gru-testproject", stored.TmuxSession)
	}

	// Verify the project was persisted on successful launch.
	projects, err := s.Queries().ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("projects = %d, want 1 — project should be persisted on success", len(projects))
	}
}

func TestService_KillSession(t *testing.T) {
	svc, s := newTestService(t)

	reg := controller.NewRegistry()
	reg.Register(&fakeSessionController{
		runtimeID: "claude-code",
		launchFn: func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
			return &controller.SessionHandle{
				SessionID:   opts.SessionID,
				TmuxSession: "gru-testproject",
				TmuxWindow:  "feat-dev·kill1234",
			}, nil
		},
	})
	svc.SetControllerRegistry(reg)

	projectDir := t.TempDir()
	launchResp, err := svc.LaunchSession(context.Background(), connect.NewRequest(&gruv1.LaunchSessionRequest{
		ProjectDir: projectDir,
		Prompt:     "do work",
		Name:       "kill-test",
	}))
	if err != nil {
		t.Fatalf("LaunchSession: %v", err)
	}
	sessionID := launchResp.Msg.Session.Id

	killResp, err := svc.KillSession(context.Background(), connect.NewRequest(&gruv1.KillSessionRequest{Id: sessionID}))
	if err != nil {
		t.Fatalf("KillSession: unexpected error: %v", err)
	}
	if !killResp.Msg.Success {
		t.Error("KillSession: Success = false, want true")
	}

	stored, err := s.Queries().GetSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetSession after kill: %v", err)
	}
	if stored.Status != "killed" {
		t.Errorf("status after kill = %q, want killed", stored.Status)
	}
	_ = s
}

func TestService_KillSession_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	svc.SetControllerRegistry(controller.NewRegistry())

	_, err := svc.KillSession(context.Background(), connect.NewRequest(&gruv1.KillSessionRequest{Id: "nonexistent-id"}))
	if err == nil {
		t.Fatal("expected error for nonexistent session, got nil")
	}
}

func TestService_KillSession_RejectsJournal(t *testing.T) {
	svc, s := newTestService(t)
	svc.SetControllerRegistry(controller.NewRegistry())

	ctx := context.Background()
	if _, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "journal", Name: "journal", Path: "/tmp/journal", Runtime: "claude-code",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "j1", ProjectID: "journal", Runtime: "claude-code", Status: "running", Role: "journal",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.KillSession(ctx, connect.NewRequest(&gruv1.KillSessionRequest{Id: "j1"}))
	if err == nil {
		t.Fatal("expected KillSession to reject journal role, got nil error")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("expected *connect.Error, got %T: %v", err, err)
	}
	if connectErr.Code() != connect.CodeFailedPrecondition {
		t.Errorf("code = %s, want FailedPrecondition", connectErr.Code())
	}

	// Row must not have been marked killed.
	stored, err := s.Queries().GetSession(ctx, "j1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "running" {
		t.Errorf("status after rejected kill = %q, want running", stored.Status)
	}
}

func TestService_LaunchSession_InvalidDir_DoesNotPersistProject(t *testing.T) {
	svc, s := newTestService(t)

	reg := controller.NewRegistry()
	reg.Register(&fakeSessionController{
		runtimeID: "claude-code",
		launchFn: func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
			t.Fatal("Launch should not be called for an invalid directory")
			return nil, nil
		},
	})
	svc.SetControllerRegistry(reg)

	req := connect.NewRequest(&gruv1.LaunchSessionRequest{
		ProjectDir: "/nonexistent/path/that/does/not/exist",
		Prompt:     "do something",
		Name:       "bad-dir-test",
	})

	_, err := svc.LaunchSession(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonexistent directory, got nil")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("error code = %v, want InvalidArgument", connect.CodeOf(err))
	}

	// The project should NOT have been persisted.
	projects, err := s.Queries().ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("projects = %d, want 0 — invalid project was persisted", len(projects))
	}
}

func TestService_LaunchSession_FileNotDir_DoesNotPersistProject(t *testing.T) {
	svc, s := newTestService(t)

	reg := controller.NewRegistry()
	reg.Register(&fakeSessionController{
		runtimeID: "claude-code",
		launchFn: func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
			t.Fatal("Launch should not be called for a file path")
			return nil, nil
		},
	})
	svc.SetControllerRegistry(reg)

	// Create a file, not a directory.
	filePath := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := connect.NewRequest(&gruv1.LaunchSessionRequest{
		ProjectDir: filePath,
		Prompt:     "do something",
		Name:       "file-not-dir-test",
	})

	_, err := svc.LaunchSession(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for file-that-is-not-a-directory, got nil")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("error code = %v, want InvalidArgument", connect.CodeOf(err))
	}

	projects, err := s.Queries().ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("projects = %d, want 0 — file path was persisted as project", len(projects))
	}
}

func TestService_LaunchSession_ControllerError_DoesNotPersistProject(t *testing.T) {
	svc, s := newTestService(t)

	reg := controller.NewRegistry()
	reg.Register(&fakeSessionController{
		runtimeID: "claude-code",
		launchFn: func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
			return nil, fmt.Errorf("tmux not available")
		},
	})
	svc.SetControllerRegistry(reg)

	projectDir := t.TempDir()
	req := connect.NewRequest(&gruv1.LaunchSessionRequest{
		ProjectDir: projectDir,
		Prompt:     "do something",
		Name:       "fail-launch-test",
	})

	_, err := svc.LaunchSession(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when controller fails, got nil")
	}

	// The project should NOT have been persisted since launch failed.
	projects, err := s.Queries().ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("projects = %d, want 0 — project was persisted despite failed launch", len(projects))
	}
}

type fakeSessionController struct {
	runtimeID string
	launchFn  func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error)
}

func (f *fakeSessionController) RuntimeID() string                      { return f.runtimeID }
func (f *fakeSessionController) Capabilities() []controller.Capability { return nil }
func (f *fakeSessionController) Launch(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
	return f.launchFn(ctx, opts)
}
