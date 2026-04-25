package artifacts_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dakshjotwani/gru/internal/artifacts"
	"github.com/dakshjotwani/gru/internal/store"
)

// TestSweepOrphans verifies the boot-time sweeper removes both
// (a) directories with no matching session row and
// (b) files with no matching artifact row.
func TestSweepOrphans(t *testing.T) {
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

	ctx := context.Background()
	_, err = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-1", Name: "p", Adapter: "host", Runtime: "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-real", ProjectID: "proj-1", Runtime: "claude-code", Status: "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Setup: real session dir with a known file; real session dir with an
	// orphan file; orphan session dir with no row.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "sess-real"), 0o700))
	must(os.MkdirAll(filepath.Join(root, "sess-orphan"), 0o700))
	must(os.WriteFile(filepath.Join(root, "sess-orphan", "leaked.bin"), []byte("x"), 0o600))
	must(os.WriteFile(filepath.Join(root, "sess-real", "ghost.bin"), []byte("x"), 0o600))

	if err := mgr.SweepOrphans(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "sess-orphan")); !os.IsNotExist(err) {
		t.Errorf("sess-orphan dir still exists; sweep should have removed it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sess-real", "ghost.bin")); !os.IsNotExist(err) {
		t.Errorf("ghost.bin still exists; sweep should have removed it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sess-real")); err != nil {
		t.Errorf("sess-real dir was unexpectedly removed: %v", err)
	}
}

// TestCreateAndLookup is a happy-path round-trip exercising the manager
// without going through HTTP — confirms the file actually lands and is
// readable by token.
func TestCreateAndLookup(t *testing.T) {
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

	ctx := context.Background()
	_, err = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-1", Name: "p", Adapter: "host", Runtime: "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-1", ProjectID: "proj-1", Runtime: "claude-code", Status: "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	pdfBytes := append([]byte("%PDF-1.4\n"), make([]byte, 100)...)
	art, err := mgr.Create(ctx, artifacts.CreateRequest{
		SessionID: "sess-1",
		Title:     "Resume",
		MimeType:  artifacts.MimePDF,
		Bytes:     pdfBytes,
		Runtime:   "claude-code",
	})
	if err != nil {
		t.Fatal(err)
	}
	if art.Url == "" {
		t.Error("url is empty")
	}

	// Capability URL token round-trip.
	token := art.Url[len("/artifacts/"):]
	row, path, err := mgr.LookupByToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != art.Id {
		t.Errorf("row.ID = %q, want %q", row.ID, art.Id)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("artifact file at %s missing: %v", path, err)
	}
}

// TestMarkdownValidation rejects non-UTF-8 bytes and bytes containing NUL.
func TestMarkdownValidation(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	mgr, err := artifacts.NewManager(s, t.TempDir(), artifacts.DefaultCaps(), nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_, _ = s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID: "proj-1", Name: "p", Adapter: "host", Runtime: "claude-code",
	})
	_, _ = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID: "sess-1", ProjectID: "proj-1", Runtime: "claude-code", Status: "running",
	})

	_, err = mgr.Create(ctx, artifacts.CreateRequest{
		SessionID: "sess-1", Title: "Bad", MimeType: artifacts.MimeMarkdown,
		Bytes: []byte("hello\x00world"),
	})
	if err == nil {
		t.Error("expected NUL byte rejection")
	}

	_, err = mgr.Create(ctx, artifacts.CreateRequest{
		SessionID: "sess-1", Title: "Bad", MimeType: artifacts.MimeMarkdown,
		Bytes: []byte{0xff, 0xfe, 0xfd}, // invalid UTF-8
	})
	if err == nil {
		t.Error("expected non-UTF-8 rejection")
	}

	// Valid markdown should pass.
	_, err = mgr.Create(ctx, artifacts.CreateRequest{
		SessionID: "sess-1", Title: "Good", MimeType: artifacts.MimeMarkdown,
		Bytes: []byte("# Hello\n\nWorld"),
	})
	if err != nil {
		t.Errorf("valid markdown rejected: %v", err)
	}
}
