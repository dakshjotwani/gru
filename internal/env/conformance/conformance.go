// Package conformance runs the shared Environment contract test suite against
// any adapter implementation. See docs/superpowers/specs/2026-04-17-gru-v2-design.md
// §Success criteria #6 for the case list.
package conformance

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/env"
)

// Suite describes one adapter-under-test.
type Suite struct {
	// Name is used in subtest naming.
	Name string

	// Adapter is the Environment to run the suite against.
	Adapter env.Environment

	// NewSpec returns a fresh EnvSpec for one test case. Each case calls
	// NewSpec(t) to avoid shared state across tests.
	NewSpec func(t *testing.T) env.EnvSpec

	// KillBackingResource optionally simulates "the backing resource is gone"
	// for case #4 (Rehydrate-after-kill). Implementations may remove files,
	// kill tmux sessions, etc. Return nil to skip the test case cleanly.
	KillBackingResource func(t *testing.T, inst env.Instance)

	// ForceLifecycleEvent triggers a non-heartbeat Event on the given instance
	// for case #7. If nil, case #7 is skipped.
	ForceLifecycleEvent func(t *testing.T, inst env.Instance)

	// SupportsEventsRespawn indicates whether the adapter's Events stream has
	// an external producer that can die and be respawned (case #8). Host
	// adapter typically returns false; command adapter returns true.
	SupportsEventsRespawn bool
}

// Run executes the full conformance suite. Call from an adapter's _test.go:
//
//	func TestConformance(t *testing.T) {
//	    conformance.Run(t, conformance.Suite{Name: "host", ...})
//	}
func Run(t *testing.T, s Suite) {
	t.Helper()
	if s.Adapter == nil || s.NewSpec == nil {
		t.Fatalf("conformance: Suite.Adapter and Suite.NewSpec are required")
	}

	t.Run(s.Name+"/CreateDestroy", func(t *testing.T) { caseCreateDestroy(t, s) })
	t.Run(s.Name+"/ExecEcho", func(t *testing.T) { caseExecEcho(t, s) })
	t.Run(s.Name+"/ExecPtyIsReal", func(t *testing.T) { caseExecPtyIsReal(t, s) })
	t.Run(s.Name+"/RehydrateAfterKill", func(t *testing.T) { caseRehydrateAfterKill(t, s) })
	t.Run(s.Name+"/RehydrateWorks", func(t *testing.T) { caseRehydrateWorks(t, s) })
	t.Run(s.Name+"/DestroyIsIdempotent", func(t *testing.T) { caseDestroyIdempotent(t, s) })
	t.Run(s.Name+"/EventsPump", func(t *testing.T) { caseEventsPump(t, s) })
	t.Run(s.Name+"/EventsRespawn", func(t *testing.T) { caseEventsRespawn(t, s) })
	t.Run(s.Name+"/StatusLifecycle", func(t *testing.T) { caseStatusLifecycle(t, s) })
}

func caseCreateDestroy(t *testing.T, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(t))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inst.ProviderRef == "" {
		t.Fatalf("Create returned empty ProviderRef")
	}
	if err := s.Adapter.Destroy(ctx, inst); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

func caseExecEcho(t *testing.T, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(t))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()
	res, err := s.Adapter.Exec(ctx, inst, []string{"sh", "-c", "echo hello"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("Exec exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !bytes.Contains(res.Stdout, []byte("hello")) {
		t.Fatalf("Exec stdout %q did not contain %q", res.Stdout, "hello")
	}
}

func caseExecPtyIsReal(t *testing.T, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(t))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()

	// Use `stty size` — it prints rows/cols on a real tty and fails on a
	// pipe. A valid real pty returns two numbers; a pipe returns "stdin
	// isn't a terminal".
	stream, err := s.Adapter.ExecPty(ctx, inst, []string{"sh", "-c", "stty size; exit"})
	if err != nil {
		t.Fatalf("ExecPty: %v", err)
	}
	defer stream.Close()

	out, err := readWithTimeout(stream, 3*time.Second)
	if err != nil && err != io.EOF {
		t.Fatalf("read pty: %v", err)
	}
	if strings.Contains(string(out), "not a terminal") {
		t.Fatalf("ExecPty was not a real pty — got %q", out)
	}
	// Two integers separated by whitespace (e.g. "24 80") confirm a tty.
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) < 2 {
		t.Fatalf("ExecPty output %q did not look like `stty size` output", out)
	}
}

func caseRehydrateAfterKill(t *testing.T, s Suite) {
	if s.KillBackingResource == nil {
		t.Skip("adapter does not provide KillBackingResource")
	}
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(t))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()

	s.KillBackingResource(t, inst)

	if _, err := s.Adapter.Rehydrate(ctx, inst.ProviderRef); err == nil {
		t.Fatalf("Rehydrate succeeded after backing resource was killed; expected error")
	}
}

func caseRehydrateWorks(t *testing.T, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(t))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()

	rehyd, err := s.Adapter.Rehydrate(ctx, inst.ProviderRef)
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	res, err := s.Adapter.Exec(ctx, rehyd, []string{"sh", "-c", "echo ok"})
	if err != nil {
		t.Fatalf("Exec on rehydrated instance: %v", err)
	}
	if res.ExitCode != 0 || !bytes.Contains(res.Stdout, []byte("ok")) {
		t.Fatalf("Exec on rehydrated: exit=%d stdout=%q", res.ExitCode, res.Stdout)
	}
}

func caseDestroyIdempotent(t *testing.T, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(t))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Adapter.Destroy(ctx, inst); err != nil {
		t.Fatalf("first Destroy: %v", err)
	}
	if err := s.Adapter.Destroy(ctx, inst); err != nil {
		t.Fatalf("second Destroy (idempotence): %v", err)
	}
}

func caseEventsPump(t *testing.T, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(t))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()

	evCh, err := s.Adapter.Events(ctx, inst)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	if s.ForceLifecycleEvent != nil {
		s.ForceLifecycleEvent(t, inst)
	}

	select {
	case evt, ok := <-evCh:
		if !ok {
			t.Fatalf("Events channel closed before any event arrived")
		}
		if evt.Kind == "" {
			t.Fatalf("event with empty Kind")
		}
	case <-time.After(2 * time.Second):
		if s.ForceLifecycleEvent != nil {
			t.Fatalf("no event received within 2s after ForceLifecycleEvent")
		}
		// If no force hook was provided, an adapter that emits nothing on
		// its own is still conformant. Not an error.
	}
}

func caseEventsRespawn(t *testing.T, s Suite) {
	if !s.SupportsEventsRespawn {
		t.Skip("adapter does not have a killable events producer")
	}
	// Populated by command-adapter tests. Host adapter skips.
	t.Skip("TODO: implement once a SupportsEventsRespawn adapter exists")
}

func caseStatusLifecycle(t *testing.T, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(t))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	st, err := s.Adapter.Status(ctx, inst)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Running {
		t.Fatalf("Status.Running=false after Create; want true")
	}
	if err := s.Adapter.Destroy(ctx, inst); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// After Destroy, Status should either return running=false or an error.
	if st2, err := s.Adapter.Status(ctx, inst); err == nil && st2.Running {
		t.Fatalf("Status.Running=true after Destroy; want false or error")
	}
}

// testCtx returns a context bounded at 30s — enough for all current cases.
func testCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// readWithTimeout reads from r until EOF or timeout. Returns whatever was
// read. Used so pty reads (which block forever on a live pty) don't hang.
func readWithTimeout(r io.Reader, d time.Duration) ([]byte, error) {
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		return out.Bytes(), err
	case <-time.After(d):
		return out.Bytes(), nil
	}
}
