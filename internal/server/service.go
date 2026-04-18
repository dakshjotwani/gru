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
	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/env/spec"
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
	if req.Msg.EnvSpec == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("env_spec is required"))
	}

	specPath, err := resolveSpecPath(req.Msg.EnvSpec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	loadedSpec, err := spec.LoadFile(specPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("load env spec %s: %w", specPath, err))
	}

	prompt := req.Msg.Prompt
	profile := req.Msg.Profile

	runtimeID := "claude-code"
	ctrl, err := s.controllerReg.Get(runtimeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no controller for runtime %q", runtimeID))
	}

	sessionID := uuid.NewString()

	// Load project config from the spec's primary workdir so agent profile
	// files (.gru/project.yaml, .gru/profiles.yaml) and skill content come
	// from the same dir the agent will be editing.
	primaryWorkdir := loadedSpec.Workdirs[0]
	projCfg, err := config.LoadProjectConfig(primaryWorkdir)
	if err != nil {
		log.Printf("LaunchSession: load project config: %v (continuing without profile)", err)
		projCfg = &config.ProjectConfig{}
	}
	agentProfile, err := projCfg.Profile(profile)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	skillContent, err := agentProfile.SkillContent(primaryWorkdir)
	if err != nil {
		log.Printf("LaunchSession: load skill content: %v (continuing without skills)", err)
	}

	handle, err := ctrl.Launch(ctx, controller.LaunchOptions{
		SessionID:   sessionID,
		Prompt:      prompt,
		Profile:     profile,
		Model:       agentProfile.Model,
		Agent:       agentProfile.Agent,
		ExtraPrompt: skillContent,
		AutoMode:    agentProfile.AutoMode,
		EnvSpec:     loadedSpec,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("launch: %w", err))
	}

	// Persist the project row keyed by spec path so ListProjects can show it.
	projectID, err := s.upsertProject(ctx, specPath, loadedSpec)
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

	sess := rowToSession(row)
	if payload, err := sessionToJSON(sess); err == nil {
		s.pub.Publish(&gruv1.SessionEvent{
			Type:      "snapshot.session",
			SessionId: sessionID,
			ProjectId: projectID,
			Runtime:   runtimeID,
			Timestamp: timestamppb.Now(),
			Payload:   payload,
		})
	} else {
		log.Printf("LaunchSession: marshal session %s for publish: %v", sessionID, err)
	}

	return connect.NewResponse(&gruv1.LaunchSessionResponse{
		Session: sess,
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

	if row.Role == "assistant" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("Gru assistant is server-managed; disable via `journal.enabled: false` in ~/.gru/server.yaml and restart the server"))
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

// DeleteSession removes a terminal session row and its events from the store.
// Rejects still-live sessions — the caller must KillSession first. The
// assistant singleton is protected (same as KillSession) so a stray UI click
// can't wipe it out.
func (s *Service) DeleteSession(
	ctx context.Context,
	req *connect.Request[gruv1.DeleteSessionRequest],
) (*connect.Response[gruv1.DeleteSessionResponse], error) {
	sessionID := req.Msg.Id
	if sessionID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session id required"))
	}
	row, err := s.store.Queries().GetSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %q not found", sessionID))
	} else if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if row.Role == "assistant" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("Gru assistant is server-managed and cannot be deleted"))
	}
	switch row.Status {
	case "completed", "errored", "killed":
		// OK
	default:
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("session is %s — kill it before deleting", row.Status))
	}
	if err := s.store.Queries().DeleteEventsForSession(ctx, sessionID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete events: %w", err))
	}
	if err := s.store.Queries().DeleteSession(ctx, sessionID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete session: %w", err))
	}
	s.pub.Publish(&gruv1.SessionEvent{
		Type:      "session.deleted",
		SessionId: sessionID,
		ProjectId: row.ProjectID,
		Runtime:   row.Runtime,
		Timestamp: timestamppb.Now(),
	})
	return connect.NewResponse(&gruv1.DeleteSessionResponse{Success: true}), nil
}

// PruneSessions deletes every terminal session row in one shot, skipping the
// assistant singleton. Publishes one session.deleted event per removed row so
// subscribed UIs can update their session map in-place.
func (s *Service) PruneSessions(
	ctx context.Context,
	req *connect.Request[gruv1.PruneSessionsRequest],
) (*connect.Response[gruv1.PruneSessionsResponse], error) {
	ids, err := s.store.Queries().ListTerminalSessionIDs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	deleted := int32(0)
	for _, id := range ids {
		// Best-effort: skip rows that fail rather than abort the whole batch.
		// A failed delete leaves stale state in the DB, which the operator can
		// retry later; aborting would leave the UI confused about how many
		// rows actually went.
		if err := s.store.Queries().DeleteEventsForSession(ctx, id); err != nil {
			log.Printf("PruneSessions: delete events %s: %v", id, err)
			continue
		}
		if err := s.store.Queries().DeleteSession(ctx, id); err != nil {
			log.Printf("PruneSessions: delete session %s: %v", id, err)
			continue
		}
		s.pub.Publish(&gruv1.SessionEvent{
			Type:      "session.deleted",
			SessionId: id,
			Timestamp: timestamppb.Now(),
		})
		deleted++
	}
	return connect.NewResponse(&gruv1.PruneSessionsResponse{DeletedCount: deleted}), nil
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
	// project_id is the spec path. Load the spec to find its primary
	// workdir, then look for agent profile files (.gru/project.yaml etc.)
	// inside that workdir.
	workdir, err := projectPrimaryWorkdir(req.Msg.ProjectId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	projCfg, err := config.LoadProjectConfig(workdir)
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
	// project_id is the spec path; suggester wants the primary workdir so
	// it can peek at the repo's README/language/etc.
	workdir, _ := projectPrimaryWorkdir(req.Msg.ProjectId)
	name, desc, err := s.suggester.suggest(ctx, req.Msg.Prompt, workdir)
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
		projects = append(projects, rowToProject(r))
	}
	return connect.NewResponse(&gruv1.ListProjectsResponse{Projects: projects}), nil
}

// UpdateProject is now a display-name rename only. Workdirs, adapter config,
// and add-dirs live in the spec file; to change them, edit the YAML. An
// empty new_name is a no-op.
func (s *Service) UpdateProject(
	ctx context.Context,
	req *connect.Request[gruv1.UpdateProjectRequest],
) (*connect.Response[gruv1.Project], error) {
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project id required"))
	}
	if req.Msg.NewName == "" {
		row, err := s.store.Queries().GetProject(ctx, req.Msg.Id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project %s not found", req.Msg.Id))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(rowToProject(row)), nil
	}
	row, err := s.store.Queries().RenameProject(ctx, store.RenameProjectParams{
		ID:   req.Msg.Id,
		Name: req.Msg.NewName,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project %s not found", req.Msg.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(rowToProject(row)), nil
}

// rowToProject converts a sqlc Project row to the protobuf Project.
func rowToProject(r db.Project) *gruv1.Project {
	return &gruv1.Project{
		Id:        r.ID,
		Name:      r.Name,
		Adapter:   r.Adapter,
		Runtime:   r.Runtime,
		CreatedAt: parseTimestamp(r.CreatedAt),
	}
}

// projectPrimaryWorkdir returns the first workdir of the spec pointed at by
// projectID (which is an absolute spec path). Used by ListProfiles and
// SuggestSessionName, both of which want the "the directory the agent will
// be editing" without carrying a workdir in the request.
func projectPrimaryWorkdir(projectID string) (string, error) {
	if projectID == "" {
		return "", errors.New("project_id is required")
	}
	loaded, err := spec.LoadFile(projectID)
	if err != nil {
		return "", fmt.Errorf("load project spec %s: %w", projectID, err)
	}
	if len(loaded.Workdirs) == 0 {
		return "", fmt.Errorf("project spec %s has no workdirs", projectID)
	}
	return loaded.Workdirs[0], nil
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

// upsertProject records a project row keyed on the absolute spec path.
// Display name is the basename of the spec's parent directory (so
// ~/.gru/projects/gru-minion-fullstack/spec.yaml → "gru-minion-fullstack").
// Adapter is cached from the loaded spec so ListProjects can label rows
// without re-reading the YAML.
func (s *Service) upsertProject(ctx context.Context, specPath string, loadedSpec env.EnvSpec) (string, error) {
	name := filepath.Base(filepath.Dir(specPath))
	if name == "" || name == "." || name == "/" {
		name = filepath.Base(specPath)
	}
	row, err := s.store.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID:      specPath,
		Name:    name,
		Adapter: loadedSpec.Adapter,
		Runtime: "claude-code",
	})
	if err != nil {
		return "", err
	}
	return row.ID, nil
}

// resolveSpecPath turns the client-supplied env_spec value into an absolute
// spec.yaml path. Accepted inputs:
//   - absolute path ending in .yaml → used as-is
//   - absolute path to a directory → append "spec.yaml"
//   - bare name "foo" → ~/.gru/projects/foo/spec.yaml
//   - relative .yaml path → resolved against $PWD
//
// The returned path MUST exist; resolveSpecPath errors otherwise.
func resolveSpecPath(input string) (string, error) {
	if input == "" {
		return "", errors.New("env_spec is empty")
	}
	var candidate string
	switch {
	case strings.HasPrefix(input, "/"):
		candidate = input
	case strings.HasSuffix(input, ".yaml") || strings.HasSuffix(input, ".yml"):
		abs, err := filepath.Abs(input)
		if err != nil {
			return "", fmt.Errorf("resolve env_spec %s: %w", input, err)
		}
		candidate = abs
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		candidate = filepath.Join(home, ".gru", "projects", input, "spec.yaml")
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("env_spec %s: %w", candidate, err)
	}
	if info.IsDir() {
		candidate = filepath.Join(candidate, "spec.yaml")
		if _, err := os.Stat(candidate); err != nil {
			return "", fmt.Errorf("env_spec dir %s has no spec.yaml: %w", candidate, err)
		}
	}
	return candidate, nil
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
