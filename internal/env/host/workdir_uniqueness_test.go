package host_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/env/host"
)

// TestWorkdirSetUniqueness verifies that the host adapter rejects a second
// Create against the same ordered workdir set, and that a Destroy frees the
// claim.
func TestWorkdirSetUniqueness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a := host.New()
	dir := t.TempDir()

	first, err := a.Create(ctx, env.EnvSpec{
		Name:     "first-" + time.Now().Format("150405"),
		Adapter:  "host",
		Workdirs: []string{dir},
	})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Second Create on the same workdir should fail.
	_, err = a.Create(ctx, env.EnvSpec{
		Name:     "second-" + time.Now().Format("150405"),
		Adapter:  "host",
		Workdirs: []string{dir},
	})
	if err == nil {
		t.Fatalf("expected ErrWorkdirSetInUse, got nil")
	}
	if !errors.Is(err, host.ErrWorkdirSetInUse) {
		t.Fatalf("expected ErrWorkdirSetInUse, got %v", err)
	}

	// Different workdir set succeeds.
	dir2 := t.TempDir()
	_, err = a.Create(ctx, env.EnvSpec{
		Name:     "third-" + time.Now().Format("150405"),
		Adapter:  "host",
		Workdirs: []string{dir2},
	})
	if err != nil {
		t.Fatalf("third Create on different workdir: %v", err)
	}

	// Destroy first and re-Create on the same path — succeeds.
	if err := a.Destroy(ctx, first); err != nil {
		t.Fatalf("Destroy first: %v", err)
	}
	_, err = a.Create(ctx, env.EnvSpec{
		Name:     "first-again-" + time.Now().Format("150405"),
		Adapter:  "host",
		Workdirs: []string{dir},
	})
	if err != nil {
		t.Fatalf("re-Create after Destroy: %v", err)
	}
}

// TestWorkdirSetOrderMatters checks that [a, b] and [b, a] are treated as
// distinct sessions — primary cwd vs. --add-dir semantics differ.
func TestWorkdirSetOrderMatters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a := host.New()
	dirA := t.TempDir()
	dirB := t.TempDir()

	if _, err := a.Create(ctx, env.EnvSpec{
		Name: "ab", Adapter: "host", Workdirs: []string{dirA, dirB},
	}); err != nil {
		t.Fatalf("Create ab: %v", err)
	}
	// Reversed order is a different set from the adapter's perspective.
	if _, err := a.Create(ctx, env.EnvSpec{
		Name: "ba", Adapter: "host", Workdirs: []string{dirB, dirA},
	}); err != nil {
		t.Fatalf("Create ba (reversed order): expected success, got %v", err)
	}
}
