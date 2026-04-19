package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeSrv struct {
	gruv1connect.UnimplementedGruServiceHandler
	sessions []*gruv1.Session
}

func (f *fakeSrv) ListSessions(_ context.Context, _ *connect.Request[gruv1.ListSessionsRequest]) (*connect.Response[gruv1.ListSessionsResponse], error) {
	return connect.NewResponse(&gruv1.ListSessionsResponse{Sessions: f.sessions}), nil
}

func (f *fakeSrv) GetSession(_ context.Context, req *connect.Request[gruv1.GetSessionRequest]) (*connect.Response[gruv1.Session], error) {
	for _, s := range f.sessions {
		if s.Id == req.Msg.Id || strings.HasPrefix(s.Id, req.Msg.Id) {
			return connect.NewResponse(s), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, nil)
}

func (f *fakeSrv) KillSession(_ context.Context, req *connect.Request[gruv1.KillSessionRequest]) (*connect.Response[gruv1.KillSessionResponse], error) {
	for _, s := range f.sessions {
		if s.Id == req.Msg.Id || strings.HasPrefix(s.Id, req.Msg.Id) {
			return connect.NewResponse(&gruv1.KillSessionResponse{Success: true}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, nil)
}

func (f *fakeSrv) LaunchSession(_ context.Context, req *connect.Request[gruv1.LaunchSessionRequest]) (*connect.Response[gruv1.LaunchSessionResponse], error) {
	sess := &gruv1.Session{
		Id:        "new-session-abc",
		ProjectId: "proj-1",
		Runtime:   "claude-code",
		Status:    gruv1.SessionStatus_SESSION_STATUS_STARTING,
		StartedAt: timestamppb.Now(),
		Name:      req.Msg.Name,
	}
	return connect.NewResponse(&gruv1.LaunchSessionResponse{Session: sess}), nil
}

func startFakeServer(t *testing.T, srv *fakeSrv) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(gruv1connect.NewGruServiceHandler(srv))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL
}

func runCLI(t *testing.T, serverURL string, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	fullArgs := append([]string{"--server", serverURL}, args...)
	root.SetArgs(fullArgs)
	if err := root.Execute(); err != nil {
		t.Fatalf("CLI error: %v", err)
	}
	return buf.String()
}

func runCLIErr(t *testing.T, serverURL string, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	fullArgs := append([]string{"--server", serverURL}, args...)
	root.SetArgs(fullArgs)
	_ = root.Execute()
	return buf.String()
}

func TestCLI_Status_List(t *testing.T) {
	now := timestamppb.New(time.Now().Add(-5 * time.Minute))
	srv := &fakeSrv{sessions: []*gruv1.Session{{
		Id:             "abcd1234-efgh-ijkl-mnop-qrstuvwxyz00",
		ProjectId:      "proj-alpha",
		Runtime:        "claude-code",
		Status:         gruv1.SessionStatus_SESSION_STATUS_RUNNING,
		AttentionScore: 0.8,
		StartedAt:      now,
	}}}
	out := runCLI(t, startFakeServer(t, srv), "status")
	if !strings.Contains(out, "abcd1234") {
		t.Errorf("output missing session ID prefix: %q", out)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("output missing status: %q", out)
	}
	if !strings.Contains(out, "proj-alpha") {
		t.Errorf("output missing project ID: %q", out)
	}
}

func TestCLI_Status_Single(t *testing.T) {
	now := timestamppb.Now()
	srv := &fakeSrv{sessions: []*gruv1.Session{{
		Id:             "abcd1234-0000-0000-0000-000000000001",
		ProjectId:      "proj-beta",
		Runtime:        "claude-code",
		Status:         gruv1.SessionStatus_SESSION_STATUS_IDLE,
		AttentionScore: 0.3,
		StartedAt:      now,
		Pid:            42,
	}}}
	out := runCLI(t, startFakeServer(t, srv), "status", "abcd1234")
	if !strings.Contains(out, "abcd1234") {
		t.Errorf("output missing session ID: %q", out)
	}
	if !strings.Contains(out, "idle") {
		t.Errorf("output missing status: %q", out)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("output missing PID: %q", out)
	}
}

func TestCLI_Kill(t *testing.T) {
	srv := &fakeSrv{sessions: []*gruv1.Session{
		{Id: "kill-me-00000-00000", Status: gruv1.SessionStatus_SESSION_STATUS_RUNNING},
	}}
	out := runCLI(t, startFakeServer(t, srv), "kill", "kill-me")
	if !strings.Contains(out, "killed") && !strings.Contains(out, "success") {
		t.Errorf("output should indicate success: %q", out)
	}
}

func TestCLI_Launch(t *testing.T) {
	srv := &fakeSrv{}
	out := runCLI(t, startFakeServer(t, srv), "launch", "--name", "test-session", "/tmp", "do something")
	if !strings.Contains(out, "test-session") {
		t.Errorf("output missing session name: %q", out)
	}
}

func TestCLI_Attach_NoTmux(t *testing.T) {
	srv := &fakeSrv{sessions: []*gruv1.Session{{
		Id:          "abcd1234-0000-0000-0000-000000000001",
		TmuxSession: "",
		TmuxWindow:  "",
	}}}
	out := runCLIErr(t, startFakeServer(t, srv), "attach", "abcd1234")
	if !strings.Contains(out, "no tmux session") {
		t.Errorf("expected 'no tmux session' in output: %q", out)
	}
}
