package store_test

import (
	"context"
	"testing"

	"github.com/dakshjotwani/gru/internal/store"
)

func TestOpen_createsSchema(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Verify tables exist by running a simple query.
	ctx := context.Background()
	projects, err := s.Queries().ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects after Open: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(projects))
	}
}

func TestStore_upsertAndGetProject(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	q := s.Queries()

	p, err := q.UpsertProject(ctx, store.UpsertProjectParams{
		ID:      "proj-1",
		Name:    "my-project",
		Adapter: "host",
		Runtime: "claude-code",
	})
	if err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if p.ID != "proj-1" {
		t.Errorf("id = %q, want %q", p.ID, "proj-1")
	}

	got, err := q.GetProject(ctx, "proj-1")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Adapter != "host" {
		t.Errorf("adapter = %q, want %q", got.Adapter, "host")
	}
}
