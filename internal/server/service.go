package server

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/store/db"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service implements gruv1connect.GruServiceHandler.
type Service struct {
	store *store.Store
	pub   *ingestion.Publisher
}

var _ gruv1connect.GruServiceHandler = (*Service)(nil)

func NewService(s *store.Store, pub *ingestion.Publisher) *Service {
	return &Service{store: s, pub: pub}
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
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("launch not yet implemented"))
}

func (s *Service) KillSession(
	ctx context.Context,
	req *connect.Request[gruv1.KillSessionRequest],
) (*connect.Response[gruv1.KillSessionResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("kill not yet implemented"))
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
	return sess
}

func sessionToJSON(sess *gruv1.Session) ([]byte, error) {
	return protojson.Marshal(sess)
}
