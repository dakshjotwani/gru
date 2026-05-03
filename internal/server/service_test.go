package server_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/controller"
	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/ingest"
	"github.com/dakshjotwani/gru/internal/publisher"
	"github.com/dakshjotwani/gru/internal/server"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
)

// writeHostSpec writes a minimal host-adapter spec into a sibling "spec"
// directory next to workdir and returns the absolute spec path. Tests use
// this when they want a real spec file to hand to LaunchSession but don't
// care what's in it beyond "host, one workdir."
func writeHostSpec(t *testing.T, workdir string) string {
	t.Helper()
	specDir := filepath.Join(workdir, ".gru-test-spec")
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(specDir, "spec.yaml")
	body := "name: testspec\nadapter: host\nworkdirs:\n  - " + workdir + "\n"
	if err := os.WriteFile(specPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return specPath
}

func newTestServer(t *testing.T) (gruv1connect.GruServiceClient, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	pub := publisher.NewPublisher(s)
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
	svc, s, _ := newTestServiceWithPub(t)
	return svc, s
}

// newTestServiceWithPub is like newTestService but also returns the Publisher so
// tests can subscribe and assert on broadcast events.
func newTestServiceWithPub(t *testing.T) (*server.Service, *store.Store, *publisher.Publisher) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	pub := publisher.NewPublisher(s)
	svc := server.NewService(s, pub)
	return svc, s, pub
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
		ID: "p1", Name: "proj", Adapter: "host", Runtime: "claude-code",
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
		ID: "p1", Name: "alpha", Adapter: "host", Runtime: "claude-code",
	})
	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "p2", Name: "beta", Adapter: "host", Runtime: "claude-code",
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
	specPath := writeHostSpec(t, projectDir)
	req := connect.NewRequest(&gruv1.LaunchSessionRequest{
		EnvSpec: specPath,
		Prompt:  "write tests",
		Profile: "default",
		Name:    "test-session",
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
	specPath := writeHostSpec(t, projectDir)
	launchResp, err := svc.LaunchSession(context.Background(), connect.NewRequest(&gruv1.LaunchSessionRequest{
		EnvSpec: specPath,
		Prompt:  "do work",
		Name:    "kill-test",
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

	// Rev-3 contract: the handler appends killed_by_user to the
	// per-session ingest log; the tailer (not running in this unit
	// test) is what flips status. So we assert on the log, not the
	// session row.
	homeDir, _ := os.UserHomeDir()
	logPath := ingest.LogPath(homeDir, sessionID)
	t.Cleanup(func() { _ = os.Remove(logPath) })
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log %s: %v", logPath, err)
	}
	if !bytes.Contains(data, []byte(`"killed_by_user"`)) {
		t.Errorf("log %s missing killed_by_user event:\n%s", logPath, data)
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
		ID: "journal", Name: "journal", Adapter: "host", Runtime: "claude-code",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "j1", ProjectID: "journal", Runtime: "claude-code", Status: "running", Role: "assistant",
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
		EnvSpec: "/nonexistent/path/that/does/not/exist/spec.yaml",
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

	// Point EnvSpec at a file that isn't a yaml — spec.LoadFile should reject.
	req := connect.NewRequest(&gruv1.LaunchSessionRequest{
		EnvSpec: filePath,
		Prompt:  "do something",
		Name:    "file-not-dir-test",
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
	specPath := writeHostSpec(t, projectDir)
	req := connect.NewRequest(&gruv1.LaunchSessionRequest{
		EnvSpec: specPath,
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

// TestService_LaunchSession_WithEnvSpec verifies the spec path flows through
// LaunchSession to the controller's LaunchOptions.EnvSpec verbatim.
func TestService_LaunchSession_WithEnvSpec(t *testing.T) {
	svc, _ := newTestService(t)

	reg := controller.NewRegistry()
	var seen env.EnvSpec
	reg.Register(&fakeSessionController{
		runtimeID: "claude-code",
		launchFn: func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
			seen = opts.EnvSpec
			return &controller.SessionHandle{SessionID: opts.SessionID}, nil
		},
	})
	svc.SetControllerRegistry(reg)

	projectDir := t.TempDir()
	specPath := filepath.Join(projectDir, "spec.yaml")
	if err := os.WriteFile(specPath, []byte(
		"name: mini\nadapter: command\nworkdirs:\n  - .\nconfig:\n  mode: fullstack\n  create: scripts/create.sh\n  exec: scripts/exec.sh\n  exec_pty: scripts/exec-pty.sh\n  destroy: scripts/destroy.sh\n",
	), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	req := connect.NewRequest(&gruv1.LaunchSessionRequest{
		EnvSpec: specPath,
		Prompt:  "hello",
		Name:    "test-minion",
	})

	if _, err := svc.LaunchSession(context.Background(), req); err != nil {
		t.Fatalf("LaunchSession: %v", err)
	}
	if seen.Adapter != "command" {
		t.Errorf("EnvSpec.Adapter = %q, want command", seen.Adapter)
	}
	if got, want := seen.Config["mode"], "fullstack"; got != want {
		t.Errorf("EnvSpec.Config[mode] = %v, want %v", got, want)
	}
}

func TestService_LaunchSession_BadEnvSpecPath(t *testing.T) {
	svc, _ := newTestService(t)
	reg := controller.NewRegistry()
	reg.Register(&fakeSessionController{
		runtimeID: "claude-code",
		launchFn: func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
			t.Fatal("Launch should not be called when env spec fails to load")
			return nil, nil
		},
	})
	svc.SetControllerRegistry(reg)

	req := connect.NewRequest(&gruv1.LaunchSessionRequest{
		EnvSpec: "/nonexistent/spec.yaml",
		Name:    "x",
		Prompt:  "hello",
	})
	_, err := svc.LaunchSession(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("error code = %v, want InvalidArgument", connect.CodeOf(err))
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

// TestService_LaunchSession_PublishesSnapshotEvent verifies that LaunchSession
// broadcasts a snapshot.session event so that subscribers already connected via
// SubscribeEvents see the new session without a page refresh.
func TestService_LaunchSession_PublishesSnapshotEvent(t *testing.T) {
	svc, _, pub := newTestServiceWithPub(t)

	pctx, pcancel := context.WithCancel(context.Background())
	defer pcancel()
	go pub.Run(pctx)

	reg := controller.NewRegistry()
	reg.Register(&fakeSessionController{
		runtimeID: "claude-code",
		launchFn: func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
			return &controller.SessionHandle{SessionID: opts.SessionID}, nil
		},
	})
	svc.SetControllerRegistry(reg)

	sub, _, err := pub.Subscribe("test-subscriber", 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer pub.Unsubscribe("test-subscriber")

	projectDir := t.TempDir()
	specPath := writeHostSpec(t, projectDir)
	resp, err := svc.LaunchSession(context.Background(), connect.NewRequest(&gruv1.LaunchSessionRequest{
		EnvSpec: specPath,
		Prompt:  "hello",
		Name:    "pub-test",
	}))
	if err != nil {
		t.Fatalf("LaunchSession: %v", err)
	}
	sessionID := resp.Msg.Session.Id

	select {
	case evt := <-sub.Events():
		if evt.Type != "snapshot.session" {
			t.Errorf("event type = %q, want snapshot.session", evt.Type)
		}
		if evt.SessionId != sessionID {
			t.Errorf("event session_id = %q, want %q", evt.SessionId, sessionID)
		}
		if len(evt.Payload) == 0 {
			t.Error("event payload is empty, want JSON-encoded Session")
		}
	case <-time.After(2 * time.Second):
		t.Error("no event published after LaunchSession; sidebar will not update without a page refresh")
	}
}
