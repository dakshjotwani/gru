package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"

	"github.com/dakshjotwani/gru/internal/artifacts"
	"github.com/spf13/cobra"
)

// newArtifactCmd is the parent for artifact subcommands. Today only
// `add` is wired; list/delete remain on the gRPC API for the dashboard.
func newArtifactCmd(s *rootState) *cobra.Command {
	c := &cobra.Command{
		Use:   "artifact",
		Short: "Surface a byte payload (PDF / Markdown) for the operator",
	}
	c.AddCommand(newArtifactAddCmd(s))
	return c
}

func newArtifactAddCmd(s *rootState) *cobra.Command {
	var title, file string
	c := &cobra.Command{
		Use:   "add",
		Short: "Upload a PDF or Markdown file as a session artifact",
		Long: `Upload a file as an artifact attached to the current session.

Reads the session ID from <cwd>/.gru/session-id and the server address
from ~/.gru/server.yaml (same lookup the Claude Code hook uses).

The MIME type is inferred from the file extension. Anything outside the
allowlist (PDF, Markdown today) is rejected.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return fmt.Errorf("--title is required")
			}
			if file == "" {
				return fmt.Errorf("--file is required")
			}

			sessionID, err := readCWDSessionID()
			if err != nil {
				return err
			}
			serverURL := s.serverURL // populated by PersistentPreRunE
			if serverURL == "" {
				return fmt.Errorf("server URL not resolved (is ~/.gru/server.yaml present?)")
			}

			data, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read %s: %w", file, err)
			}

			// MIME inference: extension first, then http.DetectContentType
			// as a sniffer fallback. Reject early if neither lands on the
			// allowlist so the caller gets a fast local error rather than
			// a 415 from the server.
			mime := mimeFromExtension(file)
			if mime == "" {
				mime = sniffMIME(data)
			}
			if mime != artifacts.MimePDF && mime != artifacts.MimeMarkdown {
				return fmt.Errorf("unsupported MIME type %q (allowlist: PDF, Markdown)", mime)
			}

			art, err := postArtifact(serverURL, sessionID, title, file, mime, data)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "uploaded %s (%d bytes) at %s\n",
				art.Title, art.SizeBytes, art.URL)
			return nil
		},
	}
	c.Flags().StringVar(&title, "title", "", "tab label shown in the dashboard (required, ≤ 80 bytes)")
	c.Flags().StringVar(&file, "file", "", "path to the file to upload (required)")
	return c
}

// artifactResponse mirrors the JSON the server returns. We don't import
// the proto type here so the CLI stays decoupled from generated code.
type artifactResponse struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	URL       string `json:"url"`
}

// postArtifact builds the multipart body and POSTs it to /artifacts.
func postArtifact(serverURL, sessionID, title, filename, mime string, data []byte) (*artifactResponse, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("title", title); err != nil {
		return nil, err
	}
	// Use the long-form CreatePart so we can set the per-part Content-Type
	// (the server treats that as the canonical MIME).
	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="content"; filename="%s"`, filepath.Base(filename)))
	hdr.Set("Content-Type", mime)
	w, err := mw.CreatePart(hdr)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(w, bytes.NewReader(data)); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", serverURL+"/artifacts", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Gru-Session-ID", sessionID)
	req.Header.Set("X-Gru-Runtime", "claude-code")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST /artifacts: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out artifactResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// mimeFromExtension returns the MIME for a file extension on the allowlist,
// or "" otherwise.
func mimeFromExtension(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return artifacts.MimePDF
	case ".md", ".markdown":
		return artifacts.MimeMarkdown
	default:
		return ""
	}
}

// sniffMIME runs http.DetectContentType and normalizes the result so a
// PDF that http detects as "application/pdf" lands on the allowlist.
// http.DetectContentType doesn't have a Markdown signature, so .md files
// without an extension hit the allowlist via mimeFromExtension only.
func sniffMIME(data []byte) string {
	sniffed := http.DetectContentType(data)
	// DetectContentType returns "application/pdf" or "application/pdf; charset=utf-8".
	if i := strings.Index(sniffed, ";"); i >= 0 {
		sniffed = sniffed[:i]
	}
	return strings.TrimSpace(sniffed)
}

// readCWDSessionID looks up <cwd>/.gru/session-id (same lookup as
// hooks/claude-code.sh). Returns an error if the file is missing —
// the agent should be running inside a session-managed working dir.
func readCWDSessionID() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	path := filepath.Join(cwd, ".gru", "session-id")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read session id from %s: %w", path, err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", fmt.Errorf("session id at %s is empty", path)
	}
	return id, nil
}
