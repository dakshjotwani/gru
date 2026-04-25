package ingestion

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/dakshjotwani/gru/internal/artifacts"
)

// ArtifactUploadHandler implements POST /artifacts. The shape mirrors the
// existing /events handler (header-based session/runtime, no auth — the
// server binds to the tailnet/loopback interface, the boundary is network
// reachability, see internal/config.Load comment about api_key removal).
type ArtifactUploadHandler struct {
	mgr *artifacts.Manager
}

// NewArtifactUploadHandler returns a handler ready to mount on POST /artifacts.
func NewArtifactUploadHandler(mgr *artifacts.Manager) http.Handler {
	return &ArtifactUploadHandler{mgr: mgr}
}

func (h *ArtifactUploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.Header.Get("X-Gru-Session-ID")
	if sessionID == "" {
		http.Error(w, "missing X-Gru-Session-ID header", http.StatusBadRequest)
		return
	}
	runtime := r.Header.Get("X-Gru-Runtime")
	if runtime == "" {
		http.Error(w, "missing X-Gru-Runtime header", http.StatusBadRequest)
		return
	}

	// Cap the in-memory body size: largest per-MIME limit + multipart slop.
	// Anything above this is rejected before we even parse the form.
	r.Body = http.MaxBytesReader(w, r.Body, h.mgr.MultipartLimitBytes())

	if err := r.ParseMultipartForm(8 << 20); err != nil {
		// http.MaxBytesError is the typed signal for "body too large".
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, fmt.Sprintf("parse multipart form: %v", err), http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "missing 'title' multipart field", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("content")
	if err != nil {
		http.Error(w, "missing 'content' multipart field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	body, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, fmt.Sprintf("read multipart content: %v", err), http.StatusBadRequest)
		return
	}

	mime := header.Header.Get("Content-Type")
	if mime == "" {
		// Fall back to filename-based inference so a CLI client that didn't
		// set a per-part Content-Type still works.
		mime = mimeFromFilename(header.Filename)
	}

	art, err := h.mgr.Create(r.Context(), artifacts.CreateRequest{
		SessionID: sessionID,
		Title:     title,
		MimeType:  mime,
		Bytes:     body,
		Runtime:   runtime,
	})
	if err != nil {
		writeArtifactError(w, err)
		return
	}

	// Echo the full proto as JSON; CLI helper parses this for the URL.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":         art.Id,
		"session_id": art.SessionId,
		"title":      art.Title,
		"mime_type":  art.MimeType,
		"size_bytes": art.SizeBytes,
		"url":        art.Url,
	})
}

// writeArtifactError maps the typed errors from artifacts.Manager.Create to
// the HTTP status codes documented in the design.
func writeArtifactError(w http.ResponseWriter, err error) {
	var capErr *artifacts.CapErr
	if errors.As(err, &capErr) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":      capErr.Reason,
			"count":      capErr.Count,
			"bytes_used": capErr.BytesUsed,
		})
		return
	}
	var mimeErr *artifacts.MimeErr
	if errors.As(err, &mimeErr) {
		http.Error(w, mimeErr.Reason, http.StatusUnsupportedMediaType)
		return
	}
	var sessErr *artifacts.SessionStateErr
	if errors.As(err, &sessErr) {
		if sessErr.NotFound {
			http.Error(w, sessErr.Reason, http.StatusNotFound)
			return
		}
		http.Error(w, sessErr.Reason, http.StatusGone)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// mimeFromFilename is a tiny extension → MIME map covering only the MVP
// allowlist. Anything else falls through to "" and the manager rejects it.
func mimeFromFilename(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return artifacts.MimePDF
	case ".md", ".markdown":
		return artifacts.MimeMarkdown
	default:
		return ""
	}
}

// ArtifactDownloadHandler implements GET /artifacts/<token>. The token is
// the only credential — anyone who holds the URL can read the bytes,
// anyone who doesn't cannot. Cross-origin allowed (`Access-Control-Allow-
// Origin: *`) so iframes from opaque origins (sandbox="") can fetch.
type ArtifactDownloadHandler struct {
	mgr *artifacts.Manager
}

// NewArtifactDownloadHandler returns a handler ready to mount on
// GET /artifacts/{token}.
func NewArtifactDownloadHandler(mgr *artifacts.Manager) http.Handler {
	return &ArtifactDownloadHandler{mgr: mgr}
}

// previewableMIMEs are the MIMEs we serve with `Content-Disposition:
// inline` so the browser embeds them in an iframe instead of forcing a
// download. Anything else gets `attachment`. Today both MVP allowlist
// MIMEs are previewable; the helper exists so adding e.g. application/zip
// later doesn't accidentally inline.
var previewableMIMEs = map[string]struct{}{
	artifacts.MimePDF:      {},
	artifacts.MimeMarkdown: {},
}

func (h *ArtifactDownloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.PathValue("token")
	if token == "" {
		http.NotFound(w, r)
		return
	}

	row, path, err := h.mgr.LookupByToken(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", row.MimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// cross-origin (not same-site) so the dashboard can iframe artifacts
	// served from a different host:port than the dashboard itself
	// (gru server commonly runs on a separate port from Vite). The
	// capability-URL token is the credential; CORP isn't load-bearing.
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	disposition := "attachment"
	if _, ok := previewableMIMEs[row.MimeType]; ok {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`,
		disposition, sanitizeFilename(row.Title, row.MimeType)))

	http.ServeFile(w, r, path)
}

// sanitizeFilename produces a Content-Disposition-safe filename. ASCII
// letters/digits + a few punctuation chars only — anything else (including
// spaces) becomes "_". A bare extension is appended if the title doesn't
// already have one matching the MIME.
func sanitizeFilename(title, mime string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	clean := b.String()
	if clean == "" {
		clean = "artifact"
	}
	switch mime {
	case artifacts.MimePDF:
		if !strings.HasSuffix(strings.ToLower(clean), ".pdf") {
			clean += ".pdf"
		}
	case artifacts.MimeMarkdown:
		if !strings.HasSuffix(strings.ToLower(clean), ".md") &&
			!strings.HasSuffix(strings.ToLower(clean), ".markdown") {
			clean += ".md"
		}
	}
	return clean
}
