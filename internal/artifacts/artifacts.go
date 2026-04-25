// Package artifacts implements the byte-payload surface (PDF / Markdown
// today; trivially extensible to more MIMEs via the allowlist below).
// It owns three responsibilities:
//
//  1. Cap + MIME validation (used by both the HTTP upload path and the
//     gRPC Add* / List* paths so they stay consistent).
//  2. Atomic file create order: insert DB row → write <id>.bin.tmp → fsync
//     → rename. Failures unwind the row so we don't leave a metadata-only
//     ghost.
//  3. Filesystem cleanup: per-session directory removal on session delete,
//     and a boot-time orphan sweep.
//
// gRPC handlers in internal/server and the HTTP handler in internal/ingestion
// both call into Manager. The Publisher dependency is optional — when
// non-nil, Manager publishes artifact.created events.
package artifacts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/store/db"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// MIME types currently on the allowlist. Extending the allowlist is
	// purely a server-side change — the wire format describes data, not
	// rendering, so the UI just gains a new branch.
	MimePDF      = "application/pdf"
	MimeMarkdown = "text/markdown"

	// Title length cap. Tabs label themselves with this; over-long titles
	// look bad in the UI and a runaway agent shouldn't be able to inject
	// arbitrarily long strings into a UI label.
	maxTitleBytes = 80
)

// Caps describes the per-session and per-MIME byte caps. Defaults documented
// in the design spec; operator can override via ~/.gru/server.yaml.
type Caps struct {
	PerSessionMaxCount int64
	PerSessionMaxBytes int64
	MimeLimits         map[string]int64
}

// DefaultCaps returns the limits documented in the design.
func DefaultCaps() Caps {
	return Caps{
		PerSessionMaxCount: 50,
		PerSessionMaxBytes: 100_000_000,
		MimeLimits: map[string]int64{
			MimePDF:      25_000_000,
			MimeMarkdown: 5_000_000,
		},
	}
}

// Publisher is the subset of ingestion.Publisher that artifacts needs.
type Publisher interface {
	Publish(*gruv1.SessionEvent)
}

// Manager wires the store, on-disk root directory, and (optional) publisher
// into a single object. The HTTP handler and the gRPC service both hold a
// pointer to one.
type Manager struct {
	store *store.Store
	root  string // ~/.gru/artifacts
	caps  Caps
	pub   Publisher
	// createMu serializes the cap-check + row-insert + file-write
	// sequence so two concurrent uploads cannot both pass the cap check
	// against the same session's pre-insert state. Single-operator
	// deployment, so contention is fine; a per-session mutex would only
	// matter if we had high parallel upload throughput, which we don't.
	createMu sync.Mutex
}

// NewManager creates a manager and ensures the root directory exists with
// 0700 perms. Caller-supplied caps; pass DefaultCaps() to use the defaults.
// pub may be nil (suppresses event publication, useful in tests).
func NewManager(s *store.Store, root string, caps Caps, pub Publisher) (*Manager, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("artifacts: mkdir %s: %w", root, err)
	}
	return &Manager{store: s, root: root, caps: caps, pub: pub}, nil
}

// CapErr signals that an upload was rejected due to a per-session cap.
// The HTTP handler maps it to 409 Conflict and surfaces count + bytes_used.
type CapErr struct {
	Reason    string
	Count     int64
	BytesUsed int64
}

func (e *CapErr) Error() string { return e.Reason }

// MimeErr signals that the declared/sniffed MIME type is not on the
// allowlist or that per-MIME validation failed (PDF magic bytes, MD UTF-8
// check). Maps to HTTP 415 Unsupported Media Type.
type MimeErr struct{ Reason string }

func (e *MimeErr) Error() string { return e.Reason }

// SessionStateErr signals that the target session is in a terminal state
// or doesn't exist. Maps to HTTP 410 Gone (terminal) or 404 Not Found.
type SessionStateErr struct {
	NotFound bool
	Reason   string
}

func (e *SessionStateErr) Error() string { return e.Reason }

// CreateRequest is the manager-side input for a new artifact.
type CreateRequest struct {
	SessionID string
	Title     string
	MimeType  string
	Bytes     []byte
	Runtime   string // for the published artifact.created event
}

// Create writes the artifact to disk + DB atomically, enforces caps, and
// publishes artifact.created. On error the partially-written row (if any)
// is rolled back. The caller should map errors to HTTP/gRPC status codes
// using the typed error checks above (CapErr, MimeErr, SessionStateErr).
func (m *Manager) Create(ctx context.Context, req CreateRequest) (*gruv1.Artifact, error) {
	// 1. Title length + presence.
	if req.Title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if len(req.Title) > maxTitleBytes {
		return nil, fmt.Errorf("title exceeds %d bytes", maxTitleBytes)
	}

	// 2. MIME allowlist + per-MIME validation.
	limit, ok := m.caps.MimeLimits[req.MimeType]
	if !ok {
		return nil, &MimeErr{Reason: fmt.Sprintf("unsupported MIME type %q (allowlist: PDF, Markdown)", req.MimeType)}
	}
	size := int64(len(req.Bytes))
	if size > limit {
		return nil, &MimeErr{Reason: fmt.Sprintf("artifact size %d exceeds %s limit %d", size, req.MimeType, limit)}
	}
	if err := validateMIME(req.MimeType, req.Bytes); err != nil {
		return nil, err
	}

	// 3. Session must exist and not be terminal.
	q := m.store.Queries()
	sess, err := q.GetSession(ctx, req.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &SessionStateErr{NotFound: true, Reason: fmt.Sprintf("session %s not found", req.SessionID)}
	} else if err != nil {
		return nil, fmt.Errorf("look up session: %w", err)
	}
	switch sess.Status {
	case "completed", "errored", "killed":
		return nil, &SessionStateErr{Reason: fmt.Sprintf("session %s is %s — uploads rejected", req.SessionID, sess.Status)}
	}

	// 4 + 5. Cap check + row insert under createMu so two concurrent
	// uploads cannot both observe the same pre-insert state and both
	// pass the cap check (unsafe even though SQLite serializes its own
	// writes — the application-level check + insert is two queries).
	m.createMu.Lock()
	defer m.createMu.Unlock()

	sums, err := q.SumArtifactsForSession(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("count artifacts: %w", err)
	}
	if sums.Count >= m.caps.PerSessionMaxCount {
		return nil, &CapErr{
			Reason:    fmt.Sprintf("per-session count cap (%d) reached", m.caps.PerSessionMaxCount),
			Count:     sums.Count,
			BytesUsed: sums.BytesUsed,
		}
	}
	if sums.BytesUsed+size > m.caps.PerSessionMaxBytes {
		return nil, &CapErr{
			Reason:    fmt.Sprintf("per-session bytes cap (%d) would be exceeded", m.caps.PerSessionMaxBytes),
			Count:     sums.Count,
			BytesUsed: sums.BytesUsed,
		}
	}

	id := uuid.NewString()
	token, err := mintToken()
	if err != nil {
		return nil, fmt.Errorf("mint token: %w", err)
	}

	row, err := q.CreateArtifact(ctx, store.CreateArtifactParams{
		ID:        id,
		SessionID: req.SessionID,
		Title:     req.Title,
		MimeType:  req.MimeType,
		SizeBytes: size,
		Token:     token,
	})
	if err != nil {
		return nil, fmt.Errorf("insert artifact row: %w", err)
	}

	// 6. Write the bytes atomically. On any failure here, delete the row
	// so we don't leave a metadata-only artifact behind.
	dir := filepath.Join(m.root, req.SessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		if delErr := q.DeleteArtifact(ctx, id); delErr != nil {
			log.Printf("artifacts: rollback row %s after mkdir error: %v", id, delErr)
		}
		return nil, fmt.Errorf("mkdir artifact dir: %w", err)
	}
	finalPath := filepath.Join(dir, id+".bin")
	tmpPath := finalPath + ".tmp"
	if err := writeFileAtomic(tmpPath, finalPath, req.Bytes); err != nil {
		if delErr := q.DeleteArtifact(ctx, id); delErr != nil {
			log.Printf("artifacts: rollback row %s after write error: %v", id, delErr)
		}
		return nil, fmt.Errorf("write artifact file: %w", err)
	}

	art := rowToProto(row)
	if m.pub != nil {
		m.publishArtifactEvent(art, req.Runtime, sess.ProjectID, "artifact.created")
	}
	return art, nil
}

// List returns the session's artifacts plus current count + total bytes.
func (m *Manager) List(ctx context.Context, sessionID string) ([]*gruv1.Artifact, int64, int64, error) {
	rows, err := m.store.Queries().ListArtifactsBySession(ctx, sessionID)
	if err != nil {
		return nil, 0, 0, err
	}
	out := make([]*gruv1.Artifact, 0, len(rows))
	var total int64
	for _, r := range rows {
		out = append(out, rowToProto(r))
		total += r.SizeBytes
	}
	return out, int64(len(rows)), total, nil
}

// Delete removes the row and unlinks the bytes. File-unlink errors are
// logged and queued for the boot-time orphan sweep — never surfaced to
// the caller, since the row is already gone.
func (m *Manager) Delete(ctx context.Context, id string) error {
	q := m.store.Queries()
	row, err := q.GetArtifact(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return &SessionStateErr{NotFound: true, Reason: fmt.Sprintf("artifact %s not found", id)}
	} else if err != nil {
		return err
	}
	if err := q.DeleteArtifact(ctx, id); err != nil {
		return err
	}
	path := filepath.Join(m.root, row.SessionID, row.ID+".bin")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("artifacts: unlink %s: %v (will be cleaned up on next boot)", path, err)
	}
	return nil
}

// LookupByToken returns the artifact whose capability token matches.
// Used by the GET /artifacts/<token> handler.
//
// TODO(public-internet): the SQLite WHERE token = ? compare short-circuits
// on the first byte mismatch, leaking a timing oracle. With 256 bits of
// entropy and local/tailnet-only binding it's not exploitable today; if
// Gru ever exposes this endpoint to the public internet, look up by an
// indexed token-prefix and constant-time-compare the rest with
// crypto/subtle.ConstantTimeCompare.
func (m *Manager) LookupByToken(ctx context.Context, token string) (db.Artifact, string, error) {
	row, err := m.store.Queries().GetArtifactByToken(ctx, token)
	if err != nil {
		return row, "", err
	}
	return row, filepath.Join(m.root, row.SessionID, row.ID+".bin"), nil
}

// Root returns the root artifacts directory; needed by the boot-time sweep.
func (m *Manager) Root() string { return m.root }

// CleanupSession removes every artifact row for the session (the FK
// CASCADE handles this on session delete already, but exporting this lets
// callers run it explicitly). Then rm -rf's the on-disk directory.
func (m *Manager) CleanupSession(ctx context.Context, sessionID string) error {
	dir := filepath.Join(m.root, sessionID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("rm artifact dir: %w", err)
	}
	return nil
}

// SweepOrphans scans the artifacts root for any directory whose name is
// not a session id, and any file whose id (stem) doesn't match an
// artifact row. Logs + removes both. Designed to run once at server boot.
func (m *Manager) SweepOrphans(ctx context.Context) error {
	q := m.store.Queries()
	entries, err := os.ReadDir(m.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			// Stray file at the root level — there shouldn't be any. Remove.
			path := filepath.Join(m.root, ent.Name())
			log.Printf("artifacts: sweep orphan file at root: %s", path)
			_ = os.Remove(path)
			continue
		}
		sessionID := ent.Name()
		// If the session no longer exists, blow the whole directory away.
		if _, err := q.GetSession(ctx, sessionID); errors.Is(err, sql.ErrNoRows) {
			path := filepath.Join(m.root, sessionID)
			log.Printf("artifacts: sweep orphan session dir: %s", path)
			_ = os.RemoveAll(path)
			continue
		} else if err != nil {
			log.Printf("artifacts: sweep look up session %s: %v", sessionID, err)
			continue
		}
		// Session exists — check each file against the artifact rows.
		ids, err := q.ListArtifactIDsBySession(ctx, sessionID)
		if err != nil {
			log.Printf("artifacts: sweep list ids %s: %v", sessionID, err)
			continue
		}
		known := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			known[id] = struct{}{}
		}
		fileEntries, err := os.ReadDir(filepath.Join(m.root, sessionID))
		if err != nil {
			log.Printf("artifacts: sweep read dir %s: %v", sessionID, err)
			continue
		}
		for _, fe := range fileEntries {
			name := fe.Name()
			// Strip .bin / .bin.tmp suffix.
			id := name
			if ext := filepath.Ext(id); ext == ".bin" || ext == ".tmp" {
				id = id[:len(id)-len(ext)]
				if filepath.Ext(id) == ".bin" {
					id = id[:len(id)-len(".bin")]
				}
			}
			if _, ok := known[id]; !ok {
				path := filepath.Join(m.root, sessionID, name)
				log.Printf("artifacts: sweep orphan file: %s", path)
				_ = os.Remove(path)
			}
		}
	}
	return nil
}

// publishArtifactEvent pushes a SessionEvent through the publisher
// carrying the FULL Artifact proto as JSON payload (per design §1) so
// UI subscribers don't have to refetch.
func (m *Manager) publishArtifactEvent(art *gruv1.Artifact, runtime, projectID, eventType string) {
	payload, err := protojson.MarshalOptions{UseEnumNumbers: true}.Marshal(art)
	if err != nil {
		log.Printf("artifacts: marshal %s for publish: %v", art.Id, err)
		return
	}
	m.pub.Publish(&gruv1.SessionEvent{
		Id:        uuid.NewString(),
		SessionId: art.SessionId,
		ProjectId: projectID,
		Runtime:   runtime,
		Type:      eventType,
		Timestamp: timestamppb.Now(),
		Payload:   payload,
	})
}

// PublishLinkEvent is a thin wrapper used by the gRPC AddSessionLink
// handler (kept here so artifacts and links emit through the same code).
func (m *Manager) PublishLinkEvent(link *gruv1.SessionLink, runtime, projectID string) {
	if m.pub == nil {
		return
	}
	payload, err := protojson.MarshalOptions{UseEnumNumbers: true}.Marshal(link)
	if err != nil {
		log.Printf("artifacts: marshal link %s for publish: %v", link.Id, err)
		return
	}
	m.pub.Publish(&gruv1.SessionEvent{
		Id:        uuid.NewString(),
		SessionId: link.SessionId,
		ProjectId: projectID,
		Runtime:   runtime,
		Type:      "session_link.created",
		Timestamp: timestamppb.Now(),
		Payload:   payload,
	})
}

// rowToProto converts a sqlc Artifact row to the protobuf message,
// computing the capability URL.
func rowToProto(r db.Artifact) *gruv1.Artifact {
	return &gruv1.Artifact{
		Id:        r.ID,
		SessionId: r.SessionID,
		Title:     r.Title,
		MimeType:  r.MimeType,
		SizeBytes: r.SizeBytes,
		Url:       "/artifacts/" + r.Token,
		CreatedAt: parseTimestamp(r.CreatedAt),
	}
}

func parseTimestamp(s string) *timestamppb.Timestamp {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return timestamppb.New(t)
}

// validateMIME runs the per-type sanity check from the design spec.
func validateMIME(mime string, b []byte) error {
	switch mime {
	case MimePDF:
		// %PDF- magic bytes (4 chars + the dash = 5 bytes).
		if len(b) < 5 || string(b[:5]) != "%PDF-" {
			return &MimeErr{Reason: "PDF magic bytes (%PDF-) missing"}
		}
		return nil
	case MimeMarkdown:
		if !utf8.Valid(b) {
			return &MimeErr{Reason: "markdown bytes are not valid UTF-8"}
		}
		for _, c := range b {
			if c == 0 {
				return &MimeErr{Reason: "markdown bytes contain a NUL byte"}
			}
		}
		return nil
	default:
		// Caller has already checked the allowlist; reaching here is a bug.
		return &MimeErr{Reason: fmt.Sprintf("no validator for MIME %q", mime)}
	}
}

// mintToken returns 32 random bytes base64url-encoded (no padding).
func mintToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

// writeFileAtomic writes data to tmpPath, fsyncs it, then renames it to
// finalPath. On any error before rename, removes the temp file. After a
// successful rename, fsyncs the parent directory so the rename is durable.
func writeFileAtomic(tmpPath, finalPath string, data []byte) error {
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	// fsync the parent directory so the rename itself is durable on crash
	// — without this, the comment promise that "after a successful rename,
	// fsyncs the parent directory" was a lie.
	dir, err := os.Open(filepath.Dir(finalPath))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	_ = dir.Close()
	return syncErr
}

// MultipartLimitBytes returns the largest allowed multipart body, used to
// size the LimitReader in the HTTP handler. Take the max of all per-MIME
// limits + a small slop for the multipart envelope (boundary, headers).
func (m *Manager) MultipartLimitBytes() int64 {
	var max int64
	for _, v := range m.caps.MimeLimits {
		if v > max {
			max = v
		}
	}
	// 256 KiB slop for multipart framing (boundaries, header lines, the
	// `title` part). Generous; the real bound is the per-MIME byte cap.
	return max + 256*1024
}

// Caps returns the configured caps (read-only access for the HTTP handler).
func (m *Manager) Caps() Caps { return m.caps }
