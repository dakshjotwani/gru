package ingestion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dakshjotwani/gru/internal/artifacts"
	"github.com/dakshjotwani/gru/internal/ingestion"
	"github.com/dakshjotwani/gru/internal/store"
)

// artifactSetup spins up an in-memory store, a temp artifact root, and an
// upload handler wired to both. Returns the handler, store, and root for
// downstream assertions.
func artifactSetup(t *testing.T) (http.Handler, *store.Store, string) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	root := t.TempDir()
	mgr, err := artifacts.NewManager(s, root, artifacts.DefaultCaps(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return ingestion.NewArtifactUploadHandler(mgr), s, root
}

// seedSession inserts a project + session row so the upload handler's
// "session must exist and not be terminal" check passes.
func seedSession(t *testing.T, s *store.Store, sessionID, status string) {
	t.Helper()
	ctx := context.Background()
	_, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-1", Name: "test", Adapter: "host", Runtime: "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID:        sessionID,
		ProjectID: "proj-1",
		Runtime:   "claude-code",
		Status:    status,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// buildMultipart returns a multipart body with title + content fields. The
// content part's Content-Type is the canonical MIME the server validates.
func buildMultipart(t *testing.T, title, mime, filename string, data []byte) (string, io.Reader) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("title", title); err != nil {
		t.Fatal(err)
	}
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", `form-data; name="content"; filename="`+filename+`"`)
	hdr.Set("Content-Type", mime)
	w, err := mw.CreatePart(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), &buf
}

func TestArtifactUpload_pdfHappyPath(t *testing.T) {
	h, s, root := artifactSetup(t)
	seedSession(t, s, "sess-1", "running")

	pdfBytes := append([]byte("%PDF-1.4\n"), make([]byte, 100)...)
	ct, body := buildMultipart(t, "Resume", artifacts.MimePDF, "resume.pdf", pdfBytes)

	req := httptest.NewRequest(http.MethodPost, "/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Gru-Session-ID", "sess-1")
	req.Header.Set("X-Gru-Runtime", "claude-code")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["title"] != "Resume" {
		t.Errorf("title = %v, want Resume", got["title"])
	}
	if got["mime_type"] != artifacts.MimePDF {
		t.Errorf("mime_type = %v, want %s", got["mime_type"], artifacts.MimePDF)
	}
	urlStr, _ := got["url"].(string)
	if !strings.HasPrefix(urlStr, "/artifacts/") {
		t.Errorf("url = %q, want /artifacts/<token>", urlStr)
	}

	// File on disk should exist with mode 0600.
	rows, err := s.Queries().ListArtifactsBySession(context.Background(), "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	path := filepath.Join(root, "sess-1", rows[0].ID+".bin")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestArtifactUpload_rejectsBadPDFMagic(t *testing.T) {
	h, s, _ := artifactSetup(t)
	seedSession(t, s, "sess-1", "running")

	// PDF MIME type but bytes don't start with %PDF-.
	bad := []byte("not a pdf")
	ct, body := buildMultipart(t, "Bogus", artifacts.MimePDF, "bogus.pdf", bad)

	req := httptest.NewRequest(http.MethodPost, "/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Gru-Session-ID", "sess-1")
	req.Header.Set("X-Gru-Runtime", "claude-code")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusUnsupportedMediaType, rr.Body.String())
	}
}

func TestArtifactUpload_capExceeded(t *testing.T) {
	// Override caps so we hit the cap on the second upload.
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	root := t.TempDir()
	caps := artifacts.Caps{
		PerSessionMaxCount: 1, // first upload OK, second must fail
		PerSessionMaxBytes: 1_000_000,
		MimeLimits: map[string]int64{
			artifacts.MimePDF:      1_000_000,
			artifacts.MimeMarkdown: 1_000_000,
		},
	}
	mgr, err := artifacts.NewManager(s, root, caps, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := ingestion.NewArtifactUploadHandler(mgr)

	seedSession(t, s, "sess-1", "running")

	upload := func() *httptest.ResponseRecorder {
		pdfBytes := append([]byte("%PDF-1.4\n"), make([]byte, 50)...)
		ct, body := buildMultipart(t, "x", artifacts.MimePDF, "x.pdf", pdfBytes)
		req := httptest.NewRequest(http.MethodPost, "/artifacts", body)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("X-Gru-Session-ID", "sess-1")
		req.Header.Set("X-Gru-Runtime", "claude-code")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	if rr := upload(); rr.Code != http.StatusCreated {
		t.Fatalf("first upload: status = %d, want %d", rr.Code, http.StatusCreated)
	}
	rr := upload()
	if rr.Code != http.StatusConflict {
		t.Fatalf("second upload: status = %d, want %d", rr.Code, http.StatusConflict)
	}
	// Cap response should expose count + bytes_used so the agent helper
	// can act on it.
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["count"] == nil || got["bytes_used"] == nil {
		t.Errorf("cap response missing count/bytes_used: %v", got)
	}
}

func TestArtifactUpload_rejectsTerminalSession(t *testing.T) {
	h, s, _ := artifactSetup(t)
	// Terminal session → POST should return 410 Gone.
	seedSession(t, s, "sess-killed", "killed")

	pdfBytes := append([]byte("%PDF-1.4\n"), make([]byte, 50)...)
	ct, body := buildMultipart(t, "x", artifacts.MimePDF, "x.pdf", pdfBytes)
	req := httptest.NewRequest(http.MethodPost, "/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Gru-Session-ID", "sess-killed")
	req.Header.Set("X-Gru-Runtime", "claude-code")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusGone {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusGone)
	}
}

func TestArtifactUpload_unknownSession(t *testing.T) {
	h, _, _ := artifactSetup(t)

	pdfBytes := append([]byte("%PDF-1.4\n"), make([]byte, 50)...)
	ct, body := buildMultipart(t, "x", artifacts.MimePDF, "x.pdf", pdfBytes)
	req := httptest.NewRequest(http.MethodPost, "/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Gru-Session-ID", "nonexistent")
	req.Header.Set("X-Gru-Runtime", "claude-code")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestArtifactUpload_unsupportedMIME(t *testing.T) {
	h, s, _ := artifactSetup(t)
	seedSession(t, s, "sess-1", "running")

	ct, body := buildMultipart(t, "x", "image/png", "x.png", []byte("\x89PNG..."))
	req := httptest.NewRequest(http.MethodPost, "/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Gru-Session-ID", "sess-1")
	req.Header.Set("X-Gru-Runtime", "claude-code")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusUnsupportedMediaType)
	}
}

func TestArtifactDownload_servesBytes(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	root := t.TempDir()
	mgr, err := artifacts.NewManager(s, root, artifacts.DefaultCaps(), nil)
	if err != nil {
		t.Fatal(err)
	}
	upload := ingestion.NewArtifactUploadHandler(mgr)
	download := ingestion.NewArtifactDownloadHandler(mgr)

	seedSession(t, s, "sess-1", "running")
	pdfBytes := append([]byte("%PDF-1.4\n"), make([]byte, 50)...)
	ct, body := buildMultipart(t, "x", artifacts.MimePDF, "x.pdf", pdfBytes)
	req := httptest.NewRequest(http.MethodPost, "/artifacts", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Gru-Session-ID", "sess-1")
	req.Header.Set("X-Gru-Runtime", "claude-code")
	rr := httptest.NewRecorder()
	upload.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("upload: status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	urlStr := resp["url"].(string)
	token := strings.TrimPrefix(urlStr, "/artifacts/")

	// We need to use the ServeMux pattern for r.PathValue to work.
	mux := http.NewServeMux()
	mux.Handle("GET /artifacts/{token}", download)

	getReq := httptest.NewRequest(http.MethodGet, "/artifacts/"+token, nil)
	getRR := httptest.NewRecorder()
	mux.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("download: status = %d, body: %s", getRR.Code, getRR.Body.String())
	}
	if got := getRR.Header().Get("Content-Type"); got != artifacts.MimePDF {
		t.Errorf("Content-Type = %q, want %q", got, artifacts.MimePDF)
	}
	if got := getRR.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if !bytes.Equal(getRR.Body.Bytes(), pdfBytes) {
		t.Errorf("body length = %d, want %d", len(getRR.Body.Bytes()), len(pdfBytes))
	}
}

func TestArtifactDownload_unknownTokenReturns404(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	mgr, err := artifacts.NewManager(s, t.TempDir(), artifacts.DefaultCaps(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /artifacts/{token}", ingestion.NewArtifactDownloadHandler(mgr))

	req := httptest.NewRequest(http.MethodGet, "/artifacts/no-such-token", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}
