package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/artifacts"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/store/db"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SetArtifactManager injects the artifact manager so the gRPC handlers can
// reuse the same caps + on-disk root the HTTP upload handler uses. Called
// once at server bootstrap.
func (s *Service) SetArtifactManager(m *artifacts.Manager) { s.artifactMgr = m }

// ── Artifacts ─────────────────────────────────────────────────────────────

func (s *Service) ListArtifacts(
	ctx context.Context,
	req *connect.Request[gruv1.ListArtifactsRequest],
) (*connect.Response[gruv1.ListArtifactsResponse], error) {
	if req.Msg.SessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}
	if s.artifactMgr == nil {
		return connect.NewResponse(&gruv1.ListArtifactsResponse{}), nil
	}
	out, count, bytesUsed, err := s.artifactMgr.List(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&gruv1.ListArtifactsResponse{
		Artifacts: out,
		Count:     int32(count),
		BytesUsed: bytesUsed,
	}), nil
}

func (s *Service) DeleteArtifact(
	ctx context.Context,
	req *connect.Request[gruv1.DeleteArtifactRequest],
) (*connect.Response[gruv1.DeleteArtifactResponse], error) {
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
	}
	if s.artifactMgr == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("artifact manager not configured"))
	}
	if err := s.artifactMgr.Delete(ctx, req.Msg.Id); err != nil {
		var sessErr *artifacts.SessionStateErr
		if errors.As(err, &sessErr) && sessErr.NotFound {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&gruv1.DeleteArtifactResponse{Success: true}), nil
}

// ── Session links ─────────────────────────────────────────────────────────

// allowedLinkSchemes are the URL schemes a session link may carry. Anything
// else is rejected at AddSessionLink time. `javascript:`, `data:`, `file:`
// are blocked because they're trivially weaponizable.
var allowedLinkSchemes = map[string]struct{}{
	"https":  {},
	"http":   {},
	"mailto": {},
}

const linksPerSessionMax = 20

func (s *Service) AddSessionLink(
	ctx context.Context,
	req *connect.Request[gruv1.AddSessionLinkRequest],
) (*connect.Response[gruv1.SessionLink], error) {
	if req.Msg.SessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}
	title := strings.TrimSpace(req.Msg.Title)
	if title == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("title is required"))
	}
	if len(title) > 80 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("title exceeds 80 bytes"))
	}
	rawURL := strings.TrimSpace(req.Msg.Url)
	if rawURL == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("url is required"))
	}
	if err := validateLinkURL(rawURL); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Session must exist (404). Terminal sessions are still allowed for
	// links — operators sometimes add a PR link after the session ended.
	q := s.store.Queries()
	sess, err := q.GetSession(ctx, req.Msg.SessionId)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found", req.Msg.SessionId))
	} else if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	count, err := q.CountSessionLinksForSession(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if count >= linksPerSessionMax {
		return nil, connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("per-session link cap (%d) reached", linksPerSessionMax))
	}

	row, err := q.CreateSessionLink(ctx, store.CreateSessionLinkParams{
		ID:        uuid.NewString(),
		SessionID: req.Msg.SessionId,
		Title:     title,
		Url:       rawURL,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	link := linkRowToProto(row)
	if s.artifactMgr != nil {
		// Use the artifact manager as the publish hub so artifacts and links
		// emit through identical code paths.
		s.artifactMgr.PublishLinkEvent(link, sess.Runtime, sess.ProjectID)
	} else if s.pub != nil {
		// Fallback: publish a thin event without the proto payload.
		s.pub.Publish(&gruv1.SessionEvent{
			Id:        uuid.NewString(),
			Type:      "session_link.created",
			SessionId: link.SessionId,
			ProjectId: sess.ProjectID,
			Runtime:   sess.Runtime,
			Timestamp: timestamppb.Now(),
		})
	}
	return connect.NewResponse(link), nil
}

func (s *Service) ListSessionLinks(
	ctx context.Context,
	req *connect.Request[gruv1.ListSessionLinksRequest],
) (*connect.Response[gruv1.ListSessionLinksResponse], error) {
	if req.Msg.SessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}
	rows, err := s.store.Queries().ListSessionLinksBySession(ctx, req.Msg.SessionId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*gruv1.SessionLink, 0, len(rows))
	for _, r := range rows {
		out = append(out, linkRowToProto(r))
	}
	return connect.NewResponse(&gruv1.ListSessionLinksResponse{Links: out}), nil
}

func (s *Service) DeleteSessionLink(
	ctx context.Context,
	req *connect.Request[gruv1.DeleteSessionLinkRequest],
) (*connect.Response[gruv1.DeleteSessionLinkResponse], error) {
	if req.Msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
	}
	q := s.store.Queries()
	if _, err := q.GetSessionLink(ctx, req.Msg.Id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("link %s not found", req.Msg.Id))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := q.DeleteSessionLink(ctx, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&gruv1.DeleteSessionLinkResponse{Success: true}), nil
}

// ── helpers ───────────────────────────────────────────────────────────────

func linkRowToProto(r db.SessionLink) *gruv1.SessionLink {
	return &gruv1.SessionLink{
		Id:        r.ID,
		SessionId: r.SessionID,
		Title:     r.Title,
		Url:       r.Url,
		CreatedAt: parseTimestamp(r.CreatedAt),
	}
}

// validateLinkURL enforces the scheme allowlist plus blocks RFC1918 / link-
// local hostnames so an agent can't trick the operator into clicking a
// link that pivots to an internal-network admin panel.
func validateLinkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if _, ok := allowedLinkSchemes[scheme]; !ok {
		return fmt.Errorf("scheme %q is not allowed (https/http/mailto only)", u.Scheme)
	}
	// mailto has no host to check.
	if scheme == "mailto" {
		return nil
	}
	if u.Host == "" {
		return errors.New("url has no host")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("url has no hostname")
	}
	// Reject IP literals that fall in private / link-local / loopback ranges.
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("private/loopback IP literal rejected: %s", host)
		}
	}
	// Hostnames that are obviously local. We don't resolve DNS — that's
	// out of scope for the v1 design (DNS rebinding is a separate problem).
	switch strings.ToLower(host) {
	case "localhost", "localhost.localdomain":
		return errors.New("localhost rejected")
	}
	return nil
}
