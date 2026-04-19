package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dakshjotwani/gru/internal/server"
)

// buildFakeDist lays out a minimal "web/dist" on disk so the SPA
// handler can serve something real. Returns the dir path.
func buildFakeDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte("<html>shell</html>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"),
		[]byte(`{"name":"Gru"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "index-abc.js"),
		[]byte("console.log('hi')"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSPAHandler_ServesExactFile(t *testing.T) {
	ts := httptest.NewServer(server.NewSPAHandler(buildFakeDist(t)))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Gru") {
		t.Errorf("body = %q; expected manifest", string(body))
	}
}

func TestSPAHandler_FallsBackToIndexForHTML(t *testing.T) {
	ts := httptest.NewServer(server.NewSPAHandler(buildFakeDist(t)))
	defer ts.Close()

	// /sessions/abc isn't a file — should get index.html because
	// the request accepts HTML.
	req, _ := http.NewRequest("GET", ts.URL+"/sessions/abc-123", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "shell") {
		t.Errorf("body = %q; expected index shell", string(body))
	}
}

func TestSPAHandler_Missing404sForAssets(t *testing.T) {
	ts := httptest.NewServer(server.NewSPAHandler(buildFakeDist(t)))
	defer ts.Close()

	// /assets/missing.js doesn't exist; request's Accept is JS,
	// NOT html — so we must NOT serve index.html; 404 instead.
	req, _ := http.NewRequest("GET", ts.URL+"/assets/missing.js", nil)
	req.Header.Set("Accept", "*/*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d; want 404 so broken bundles fail loudly", resp.StatusCode)
	}
}

func TestSPAHandler_PathTraversalBlocked(t *testing.T) {
	dir := buildFakeDist(t)
	// Place a file OUTSIDE the dist root the handler must never leak.
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.txt"),
		[]byte("do-not-leak"), 0644); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.NewSPAHandler(dir))
	defer ts.Close()

	// go's http.FileServer already normalizes this; we just verify
	// the handler produces either a 404 or a redirect that stays
	// within the dist root — never the secret body.
	resp, err := http.Get(ts.URL + "/../secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "do-not-leak") {
		t.Fatalf("server leaked file outside root: %q", string(body))
	}
}

func TestFindWebDist_EnvVarWins(t *testing.T) {
	dir := buildFakeDist(t)
	t.Setenv("GRU_WEB_DIST", dir)
	got := server.FindWebDist()
	if got != dir {
		t.Errorf("FindWebDist() = %q; want %q", got, dir)
	}
}

func TestFindWebDist_NotFoundReturnsEmpty(t *testing.T) {
	t.Setenv("GRU_WEB_DIST", filepath.Join(t.TempDir(), "nope"))
	// Also chdir to a temp dir so the "cwd/web/dist" fallback misses.
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	_ = os.Chdir(t.TempDir())
	if got := server.FindWebDist(); got != "" {
		t.Errorf("FindWebDist() = %q; want empty", got)
	}
}
