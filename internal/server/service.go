package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/config"
	"github.com/dakshjotwani/gru/internal/controller"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/store/db"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// nameSuggester is an interface for AI-powered session name suggestion.
// It is a single-method interface so it can be mocked in tests.
type nameSuggester interface {
	suggest(ctx context.Context, prompt, projectDir string) (name, description string, err error)
}

// claudeCLISuggester calls `claude -p` to produce a session name + description.
// It requires no API key — it uses the Claude Code CLI's existing auth.
type claudeCLISuggester struct{}

const claudeCLIPromptTemplate = `You generate concise session names and descriptions for coding agent sessions.
Output a JSON object only, with no other text before or after it.

Task: %s%s

JSON format: {"name": "kebab-case-name", "description": "one sentence"}
Name rules: kebab-case, 2-5 words, descriptive not imperative (e.g. auth-token-expiry not fix-auth-token-expiry)`

func (c *claudeCLISuggester) suggest(ctx context.Context, prompt, projectDir string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	projectSuffix := ""
	if projectDir != "" {
		projectSuffix = "\nProject directory: " + projectDir
	}
	cliPrompt := fmt.Sprintf(claudeCLIPromptTemplate, prompt, projectSuffix)

	//nolint:gosec // prompt is internal server-side content, not user-controlled shell input
	cmd := exec.CommandContext(ctx, "claude", "-p", cliPrompt)
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("claude -p: %w", err)
	}

	outStr := strings.TrimSpace(string(out))

	var result struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(outStr), &result); err != nil {
		// Claude sometimes wraps JSON in a code fence or adds a preamble — extract it.
		start := strings.Index(outStr, "{")
		end := strings.LastIndex(outStr, "}")
		if start >= 0 && end > start {
			if err2 := json.Unmarshal([]byte(outStr[start:end+1]), &result); err2 != nil {
				return "", "", fmt.Errorf("parse claude output: %w", err2)
			}
		} else {
			return "", "", fmt.Errorf("no JSON in claude output: %q", outStr)
		}
	}

	if result.Name == "" {
		return "", "", errors.New("empty name in claude output")
	}
	return result.Name, result.Description, nil
}

// Service implements gruv1connect.GruServiceHandler.
type Service struct {
	store         *store.Store
	pub           *ingestion.Publisher
	controllerReg *controller.Registry
	suggester     nameSuggester // nil means feature disabled
}

var _ gruv1connect.GruServiceHandler = (*Service)(nil)

func NewService(s *store.Store, pub *ingestion.Publisher) *Service {
	return &Service{
		store:         s,
		pub:           pub,
		controllerReg: controller.NewRegistry(),
		suggester:     &claudeCLISuggester{},
	}
}

// setSuggester replaces the suggester — used in tests.
func (s *Service) setSuggester(sg nameSuggester) {
	s.suggester = sg
}

func (s *Service) SetControllerRegistry(reg *controller.Registry) {
	s.controllerReg = reg
}

func (s *Service) ListSessions(
	ctx context.Context,
	req *connect.Request[gruv1.ListSessionsRequest],
) (*connect.Response[gruv1.ListSessionsResponse], error) {
	rows, err := s.store.Queries().ListSessions(ctx, store.ListSessionsParams{
		ProjectID: req.Msg.ProjectId,
		Status:    statusToString(req.Msg.Status),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	sessions := make([]*gruv1.Session, 0, len(rows))
	for _, r := range rows {
		sessions = append(sessions, rowToSession(r))
	}
	return connect.NewResponse(&gruv1.ListSessionsResponse{Sessions: sessions}), nil
}

func (s *Service) GetSession(
	ctx context.Context,
	req *connect.Request[gruv1.GetSessionRequest],
) (*connect.Response[gruv1.Session], error) {
	row, err := s.store.Queries().GetSession(ctx, req.Msg.Id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(rowToSession(row)), nil
}

func (s *Service) LaunchSession(
	ctx context.Context,
	req *connect.Request[gruv1.LaunchSessionRequest],
) (*connect.Response[gruv1.LaunchSessionResponse], error) {
	if req.Msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}

	projectDir := filepath.Clean(req.Msg.ProjectDir)
	prompt := req.Msg.Prompt
	profile := req.Msg.Profile

	// Validate the project directory exists before persisting anything.
	if info, err := os.Stat(projectDir); err != nil {
		if os.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project directory does not exist: %s", projectDir))
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cannot access project directory: %w", err))
	} else if !info.IsDir() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("project path is not a directory: %s", projectDir))
	}

	runtimeID := "claude-code"
	ctrl, err := s.controllerReg.Get(runtimeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no controller for runtime %q", runtimeID))
	}

	sessionID := uuid.NewString()

	// Load project config and resolve agent profile if specified.
	projCfg, err := config.LoadProjectConfig(projectDir)
	if err != nil {
		log.Printf("LaunchSession: load project config: %v (continuing without profile)", err)
		projCfg = &config.ProjectConfig{}
	}
	agentProfile, err := projCfg.Profile(profile)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	skillContent, err := agentProfile.SkillContent(projectDir)
	if err != nil {
		log.Printf("LaunchSession: load skill content: %v (continuing without skills)", err)
	}

	handle, err := ctrl.Launch(ctx, controller.LaunchOptions{
		SessionID:       sessionID,
		ProjectDir:      projectDir,
		Prompt:          prompt,
		Profile:         profile,
		Model:           agentProfile.Model,
		Agent:           agentProfile.Agent,
		ExtraPrompt:     skillContent,
		AutoMode:        agentProfile.AutoMode,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("launch: %w", err))
	}

	// Only persist the project after a successful launch — this prevents
	// invalid or nonexistent paths from polluting the project list.
	projectID, err := s.upsertProject(ctx, projectDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("upsert project: %w", err))
	}

	nilStr := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}

	row, err := s.store.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID:          sessionID,
		ProjectID:   projectID,
		Runtime:     runtimeID,
		Status:      "starting",
		Profile:     nilStr(profile),
		TmuxSession: nilStr(handle.TmuxSession),
		TmuxWindow:  nilStr(handle.TmuxWindow),
		Name:        req.Msg.Name,
		Description: req.Msg.Description,
		Prompt:      prompt,
		Role:        "",
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session row: %w", err))
	}

	return connect.NewResponse(&gruv1.LaunchSessionResponse{
		Session: rowToSession(row),
	}), nil
}

func (s *Service) KillSession(
	ctx context.Context,
	req *connect.Request[gruv1.KillSessionRequest],
) (*connect.Response[gruv1.KillSessionResponse], error) {
	sessionID := req.Msg.Id
	row, err := s.store.Queries().GetSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %q not found", sessionID))
	} else if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if row.Role == "journal" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("journal session is server-managed; disable via `journal.enabled: false` in ~/.gru/server.yaml and restart the server"))
	}

	// Route through the controller's Killer when supported so it can release
	// env-adapter resource claims (e.g. workdir-set uniqueness) in addition to
	// tearing down the tmux session. Fall back to a best-effort direct tmux
	// kill for pre-v2 sessions whose controller was never registered.
	killed := false
	if ctrl, err := s.controllerReg.Get(row.Runtime); err == nil {
		if killer, ok := ctrl.(controller.Killer); ok {
			if kErr := killer.Kill(ctx, sessionID); kErr == nil {
				killed = true
			} else {
				log.Printf("KillSession: controller.Kill(%s): %v (falling back to direct tmux)", sessionID, kErr)
			}
		}
	}
	if !killed && row.TmuxSession != nil {
		target := *row.TmuxSession
		if row.TmuxWindow != nil && *row.TmuxWindow != "" {
			target = target + ":" + *row.TmuxWindow
		}
		// kill-session for v2 (one-session-per-window); kill-window for v1.
		if row.TmuxWindow != nil && *row.TmuxWindow != "" {
			_ = exec.Command("tmux", "kill-window", "-t", target).Run()
		} else {
			_ = exec.Command("tmux", "kill-session", "-t", target).Run()
		}
	}

	_, err = s.store.Queries().UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		Status: "killed",
		ID:     sessionID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update status: %w", err))
	}

	s.pub.Publish(&gruv1.SessionEvent{
		SessionId: sessionID,
		ProjectId: row.ProjectID,
		Runtime:   row.Runtime,
		Type:      "session.killed",
		Timestamp: timestamppb.Now(),
	})

	return connect.NewResponse(&gruv1.KillSessionResponse{Success: true}), nil
}

func (s *Service) SendInput(
	ctx context.Context,
	req *connect.Request[gruv1.SendInputRequest],
) (*connect.Response[gruv1.SendInputResponse], error) {
	sessionID := req.Msg.SessionId
	row, err := s.store.Queries().GetSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %q not found", sessionID))
	} else if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Only allow input to active sessions.
	switch row.Status {
	case "running", "idle", "needs_attention":
		// OK
	default:
		return connect.NewResponse(&gruv1.SendInputResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("session is %s, cannot send input", row.Status),
		}), nil
	}

	if row.TmuxSession == nil || *row.TmuxSession == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("session has no tmux pane"))
	}

	target := *row.TmuxSession
	if row.TmuxWindow != nil && *row.TmuxWindow != "" {
		target = target + ":" + *row.TmuxWindow
	}

	// Use -l for literal text (prevents tmux from interpreting key names).
	if err := exec.Command("tmux", "send-keys", "-t", target, "-l", "--", req.Msg.Text).Run(); err != nil {
		return connect.NewResponse(&gruv1.SendInputResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("send-keys failed: %v", err),
		}), nil
	}

	// Send Enter separately.
	if err := exec.Command("tmux", "send-keys", "-t", target, "Enter").Run(); err != nil {
		return connect.NewResponse(&gruv1.SendInputResponse{
			Success:      false,
			ErrorMessage: fmt.Sprintf("send Enter failed: %v", err),
		}), nil
	}

	return connect.NewResponse(&gruv1.SendInputResponse{Success: true}), nil
}

func (s *Service) ListProfiles(
	ctx context.Context,
	req *connect.Request[gruv1.ListProfilesRequest],
) (*connect.Response[gruv1.ListProfilesResponse], error) {
	projectDir := filepath.Clean(req.Msg.ProjectDir)
	projCfg, err := config.LoadProjectConfig(projectDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load project config: %w", err))
	}
	profiles := make([]*gruv1.AgentProfile, 0, len(projCfg.Project.AgentProfiles))
	for name, p := range projCfg.Project.AgentProfiles {
		profiles = append(profiles, &gruv1.AgentProfile{
			Name:        name,
			Description: p.Description,
			Model:       p.Model,
		})
	}
	return connect.NewResponse(&gruv1.ListProfilesResponse{Profiles: profiles}), nil
}

func (s *Service) SuggestSessionName(
	ctx context.Context,
	req *connect.Request[gruv1.SuggestSessionNameRequest],
) (*connect.Response[gruv1.SuggestSessionNameResponse], error) {
	if s.suggester == nil {
		return connect.NewResponse(&gruv1.SuggestSessionNameResponse{}), nil
	}
	name, desc, err := s.suggester.suggest(ctx, req.Msg.Prompt, req.Msg.ProjectDir)
	if err != nil {
		log.Printf("SuggestSessionName: %v", err)
		return connect.NewResponse(&gruv1.SuggestSessionNameResponse{}), nil
	}
	return connect.NewResponse(&gruv1.SuggestSessionNameResponse{
		Name:        name,
		Description: desc,
	}), nil
}

func (s *Service) ListProjects(
	ctx context.Context,
	req *connect.Request[gruv1.ListProjectsRequest],
) (*connect.Response[gruv1.ListProjectsResponse], error) {
	rows, err := s.store.Queries().ListProjects(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	projects := make([]*gruv1.Project, 0, len(rows))
	for _, r := range rows {
		projects = append(projects, &gruv1.Project{
			Id:        r.ID,
			Name:      r.Name,
			Path:      r.Path,
			Runtime:   r.Runtime,
			CreatedAt: parseTimestamp(r.CreatedAt),
		})
	}
	return connect.NewResponse(&gruv1.ListProjectsResponse{Projects: projects}), nil
}

// SubscribeEvents sends a snapshot of current sessions, then streams new events.
func (s *Service) SubscribeEvents(
	ctx context.Context,
	req *connect.Request[gruv1.SubscribeEventsRequest],
	stream *connect.ServerStream[gruv1.SessionEvent],
) error {
	// TODO(phase-1c): apply req.Msg.ProjectIds and req.Msg.MinAttention filters
	// to both the snapshot and the live stream.
	rows, err := s.store.Queries().ListSessions(ctx, store.ListSessionsParams{})
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for _, row := range rows {
		sess := rowToSession(row)
		payload, err := sessionToJSON(sess)
		if err != nil {
			log.Printf("SubscribeEvents: marshal session %s: %v", row.ID, err)
		}
		if err := stream.Send(&gruv1.SessionEvent{
			Type:      "snapshot.session",
			SessionId: row.ID,
			ProjectId: row.ProjectID,
			Runtime:   row.Runtime,
			Payload:   payload,
		}); err != nil {
			return err
		}
	}

	subID := req.Header().Get("Grpc-Metadata-Sub-Id")
	if subID == "" {
		subID = req.Peer().Addr
	}
	ch := make(chan *gruv1.SessionEvent, 64)
	s.pub.Subscribe(subID, ch)
	defer s.pub.Unsubscribe(subID)

	for {
		select {
		case <-ctx.Done():
			return nil
		case evt := <-ch:
			if err := stream.Send(evt); err != nil {
				return err
			}
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func (s *Service) upsertProject(ctx context.Context, projectDir string) (string, error) {
	name := filepath.Base(projectDir)
	row, err := s.store.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID:      uuid.NewString(),
		Name:    name,
		Path:    projectDir,
		Runtime: "claude-code",
	})
	if err != nil {
		return "", err
	}
	return row.ID, nil
}

func statusToString(s gruv1.SessionStatus) string {
	switch s {
	case gruv1.SessionStatus_SESSION_STATUS_STARTING:
		return "starting"
	case gruv1.SessionStatus_SESSION_STATUS_RUNNING:
		return "running"
	case gruv1.SessionStatus_SESSION_STATUS_IDLE:
		return "idle"
	case gruv1.SessionStatus_SESSION_STATUS_NEEDS_ATTENTION:
		return "needs_attention"
	case gruv1.SessionStatus_SESSION_STATUS_COMPLETED:
		return "completed"
	case gruv1.SessionStatus_SESSION_STATUS_ERRORED:
		return "errored"
	case gruv1.SessionStatus_SESSION_STATUS_KILLED:
		return "killed"
	default:
		return "" // UNSPECIFIED → all statuses
	}
}

func stringToStatus(s string) gruv1.SessionStatus {
	switch s {
	case "starting":
		return gruv1.SessionStatus_SESSION_STATUS_STARTING
	case "running":
		return gruv1.SessionStatus_SESSION_STATUS_RUNNING
	case "idle":
		return gruv1.SessionStatus_SESSION_STATUS_IDLE
	case "needs_attention":
		return gruv1.SessionStatus_SESSION_STATUS_NEEDS_ATTENTION
	case "completed":
		return gruv1.SessionStatus_SESSION_STATUS_COMPLETED
	case "errored":
		return gruv1.SessionStatus_SESSION_STATUS_ERRORED
	case "killed":
		return gruv1.SessionStatus_SESSION_STATUS_KILLED
	default:
		return gruv1.SessionStatus_SESSION_STATUS_UNSPECIFIED
	}
}

func parseTimestamp(s string) *timestamppb.Timestamp {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return timestamppb.New(t)
}

func rowToSession(r db.Session) *gruv1.Session {
	sess := &gruv1.Session{
		Id:             r.ID,
		ProjectId:      r.ProjectID,
		Runtime:        r.Runtime,
		Status:         stringToStatus(r.Status),
		AttentionScore: r.AttentionScore,
		StartedAt:      parseTimestamp(r.StartedAt),
	}
	if r.Profile != nil {
		sess.Profile = *r.Profile
	}
	if r.EndedAt != nil {
		sess.EndedAt = parseTimestamp(*r.EndedAt)
	}
	if r.LastEventAt != nil {
		sess.LastEventAt = parseTimestamp(*r.LastEventAt)
	}
	if r.Pid != nil {
		sess.Pid = int32(*r.Pid)
	}
	if r.TmuxSession != nil {
		sess.TmuxSession = *r.TmuxSession
	}
	if r.TmuxWindow != nil {
		sess.TmuxWindow = *r.TmuxWindow
	}
	sess.Name = r.Name
	sess.Description = r.Description
	sess.Prompt = r.Prompt
	sess.Role = r.Role
	return sess
}

func sessionToJSON(sess *gruv1.Session) ([]byte, error) {
	// UseEnumNumbers emits numeric values (e.g. 1) rather than string names
	// (e.g. "SESSION_STATUS_STARTING") so the frontend's numeric TypeScript
	// enum comparisons work correctly.
	return protojson.MarshalOptions{UseEnumNumbers: true}.Marshal(sess)
}
