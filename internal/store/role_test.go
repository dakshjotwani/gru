package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dakshjotwani/gru/internal/store"
)

func TestStore_GetJournalSession(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	q := s.Queries()

	// Need a project first (foreign key).
	if _, err := q.UpsertProject(ctx, store.UpsertProjectParams{
		ID: "journal", Name: "journal", Path: "/tmp/journal", Runtime: "claude-code",
	}); err != nil {
		t.Fatal(err)
	}

	// No journal session yet → query returns sql.ErrNoRows.
	_, err = q.GetJournalSession(ctx)
	if err == nil {
		t.Fatal("expected error when no journal session exists, got nil")
	}

	// Insert a dead journal row — it should still be filtered out.
	endedAt := "2026-04-14T10:00:00Z"
	if _, err := q.CreateSession(ctx, store.CreateSessionParams{
		ID:        "j-dead",
		ProjectID: "journal",
		Runtime:   "claude-code",
		Status:    "errored",
		Role:      "journal",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		Status:  "errored",
		EndedAt: &endedAt,
		ID:      "j-dead",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = q.GetJournalSession(ctx)
	if err == nil {
		t.Fatal("expected error when only dead journal sessions exist, got nil")
	}

	// Insert a live journal row — it should be returned.
	if _, err := q.CreateSession(ctx, store.CreateSessionParams{
		ID:        "j-live",
		ProjectID: "journal",
		Runtime:   "claude-code",
		Status:    "running",
		Role:      "journal",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := q.GetJournalSession(ctx)
	if err != nil {
		t.Fatalf("GetJournalSession: %v", err)
	}
	if got.ID != "j-live" {
		t.Errorf("returned journal id = %q, want %q", got.ID, "j-live")
	}
	if got.Role != "journal" {
		t.Errorf("returned role = %q, want %q", got.Role, "journal")
	}
}

func TestStore_MigrationIdempotent(t *testing.T) {
	// Opening the same on-disk DB twice must not fail — the second Open is
	// equivalent to a server restart.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gru.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	// Sanity: queries still work after re-opening.
	if _, err := s2.Queries().ListProjects(context.Background()); err != nil {
		t.Fatalf("ListProjects after reopen: %v", err)
	}
}

func TestStore_RoleDefaultEmpty(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	q := s.Queries()
	if _, err := q.UpsertProject(ctx, store.UpsertProjectParams{
		ID: "p1", Name: "p1", Path: "/tmp/p1", Runtime: "claude-code",
	}); err != nil {
		t.Fatal(err)
	}
	// Create a session with no role explicitly set (empty string).
	row, err := q.CreateSession(ctx, store.CreateSessionParams{
		ID:        "s1",
		ProjectID: "p1",
		Runtime:   "claude-code",
		Status:    "starting",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if row.Role != "" {
		t.Errorf("default role = %q, want empty string", row.Role)
	}
}
