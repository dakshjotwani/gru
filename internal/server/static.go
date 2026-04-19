package server

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// FindWebDist returns the absolute path to a directory containing a
// built frontend (`index.html`, static assets) or "" if none is
// found. Search order:
//
//	1. The WEB_DIST environment variable (explicit override).
//	2. <cwd>/web/dist — the typical layout when running from the repo.
//	3. <executable-dir>/web/dist — when the binary ships with the
//	   frontend next to it.
//
// An empty return is normal in dev mode (Vite runs the frontend on a
// separate port); the server just doesn't mount the static handler.
func FindWebDist() string {
	candidates := []string{}
	if env := os.Getenv("GRU_WEB_DIST"); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates, "web/dist")
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "web", "dist"))
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			if _, err := os.Stat(filepath.Join(abs, "index.html")); err == nil {
				return abs
			}
		}
	}
	return ""
}

// SPAHandler serves files from root. For paths that don't map to a
// file on disk AND look like SPA routes (Accept: text/html), it
// serves root/index.html so client-side React Router can take over.
// 404s on unknown asset paths (no HTML fallback) so missing bundle
// files fail loudly instead of silently returning the shell.
type SPAHandler struct {
	root string
	fs   http.Handler
}

// NewSPAHandler returns a handler serving a single-page app from
// root. The caller is responsible for mounting it at "/" and
// ensuring all API handlers (/events, /devices, etc.) are
// registered before it so go's ServeMux picks the more specific
// pattern first.
func NewSPAHandler(root string) *SPAHandler {
	return &SPAHandler{
		root: root,
		fs:   http.FileServer(http.Dir(root)),
	}
}

func (h *SPAHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Normalize — block path traversal by rejecting any cleaned
	// path that escapes the root prefix.
	clean := filepath.Clean(r.URL.Path)
	if !strings.HasPrefix(clean, "/") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	full := filepath.Join(h.root, clean)
	if !strings.HasPrefix(full, h.root) {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}

	if info, err := os.Stat(full); err == nil && !info.IsDir() {
		// Exact file match — let http.FileServer handle content type,
		// If-Modified-Since, and range headers.
		h.fs.ServeHTTP(w, r)
		return
	}

	// SPA fallback: only for HTML navigations. Anything else (missing
	// /assets/x.js, a mis-spelled /manifest.json) 404s so the bug is
	// obvious instead of silently returning index.html as JS.
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.ServeFile(w, r, filepath.Join(h.root, "index.html"))
		return
	}
	http.NotFound(w, r)
}

// LogServingStatic prints a one-liner at startup so operators
// know whether the backend is serving its own frontend or
// expecting an external Vite dev server.
func LogServingStatic(path string) {
	if path == "" {
		log.Printf("no built frontend found (GRU_WEB_DIST / web/dist missing) — expecting Vite dev on a separate port")
		return
	}
	log.Printf("serving built frontend from %s", path)
}
