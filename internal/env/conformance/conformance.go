// Package conformance runs the shared Environment contract test suite against
// any adapter implementation. See docs/superpowers/specs/2026-04-17-gru-v2-design.md
// §Success criteria #6 for the case list.
//
// The suite is split into pure case functions (each taking a minimal Reporter)
// and a testing.T adapter layered on top. This lets the same cases drive both
// `go test` runs and the `gru env test` CLI.
package conformance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/env"
)

// Suite describes one adapter-under-test.
type Suite struct {
	Name                  string
	Adapter               env.Environment
	NewSpec               func(r Reporter) env.EnvSpec
	KillBackingResource   func(r Reporter, inst env.Instance)
	ForceLifecycleEvent   func(r Reporter, inst env.Instance)
	SupportsEventsRespawn bool
}

// Reporter is the minimal surface case functions need from their test runner.
// testing.T satisfies this by implementing Helper/Fatalf/Logf/Skip; our CLI
// runner provides a bespoke implementation.
type Reporter interface {
	Helper()
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
	Skip(args ...any)
	// Failed returns whether Fatalf has been called.
	Failed() bool
}

// CaseFunc is the signature of a single conformance case.
type CaseFunc func(r Reporter, s Suite)

// Cases returns the case list in deterministic order.
func Cases() []struct {
	Name string
	Fn   CaseFunc
} {
	return []struct {
		Name string
		Fn   CaseFunc
	}{
		{"CreateDestroy", caseCreateDestroy},
		{"ExecEcho", caseExecEcho},
		{"ExecPtyIsReal", caseExecPtyIsReal},
		{"RehydrateAfterKill", caseRehydrateAfterKill},
		{"RehydrateWorks", caseRehydrateWorks},
		{"DestroyIsIdempotent", caseDestroyIdempotent},
		{"EventsPump", caseEventsPump},
		{"EventsRespawn", caseEventsRespawn},
		{"StatusLifecycle", caseStatusLifecycle},
	}
}

// Run executes the full suite via testing.T subtests.
func Run(t *testing.T, s Suite) {
	t.Helper()
	if s.Adapter == nil || s.NewSpec == nil {
		t.Fatalf("conformance: Suite.Adapter and Suite.NewSpec are required")
	}
	for _, c := range Cases() {
		c := c
		t.Run(s.Name+"/"+c.Name, func(t *testing.T) {
			c.Fn(&testingReporter{t: t}, s)
		})
	}
}

// testingReporter adapts *testing.T to Reporter.
type testingReporter struct{ t *testing.T }

func (r *testingReporter) Helper()                             { r.t.Helper() }
func (r *testingReporter) Fatalf(format string, args ...any)   { r.t.Fatalf(format, args...) }
func (r *testingReporter) Logf(format string, args ...any)     { r.t.Logf(format, args...) }
func (r *testingReporter) Skip(args ...any)                    { r.t.Skip(args...) }
func (r *testingReporter) Failed() bool                        { return r.t.Failed() }

// ---- cases ----

func caseCreateDestroy(r Reporter, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(r))
	if err != nil {
		r.Fatalf("Create: %v", err)
	}
	if inst.ProviderRef == "" {
		r.Fatalf("Create returned empty ProviderRef")
	}
	if err := s.Adapter.Destroy(ctx, inst); err != nil {
		r.Fatalf("Destroy: %v", err)
	}
}

func caseExecEcho(r Reporter, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(r))
	if err != nil {
		r.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()
	res, err := s.Adapter.Exec(ctx, inst, []string{"sh", "-c", "echo hello"})
	if err != nil {
		r.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		r.Fatalf("Exec exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !bytes.Contains(res.Stdout, []byte("hello")) {
		r.Fatalf("Exec stdout %q did not contain %q", res.Stdout, "hello")
	}
}

func caseExecPtyIsReal(r Reporter, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(r))
	if err != nil {
		r.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()

	stream, err := s.Adapter.ExecPty(ctx, inst, []string{"sh", "-c", "stty size; exit"})
	if err != nil {
		r.Fatalf("ExecPty: %v", err)
	}
	defer stream.Close()

	out, err := readWithTimeout(stream, 3*time.Second)
	if err != nil && !errors.Is(err, io.EOF) {
		r.Fatalf("read pty: %v", err)
	}
	if strings.Contains(string(out), "not a terminal") {
		r.Fatalf("ExecPty was not a real pty — got %q", out)
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) < 2 {
		r.Fatalf("ExecPty output %q did not look like `stty size` output", out)
	}
}

func caseRehydrateAfterKill(r Reporter, s Suite) {
	if s.KillBackingResource == nil {
		r.Skip("adapter does not provide KillBackingResource")
		return
	}
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(r))
	if err != nil {
		r.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()

	s.KillBackingResource(r, inst)

	if _, err := s.Adapter.Rehydrate(ctx, inst.ProviderRef); err == nil {
		r.Fatalf("Rehydrate succeeded after backing resource was killed; expected error")
	}
}

func caseRehydrateWorks(r Reporter, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(r))
	if err != nil {
		r.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()

	rehyd, err := s.Adapter.Rehydrate(ctx, inst.ProviderRef)
	if err != nil {
		r.Fatalf("Rehydrate: %v", err)
	}
	res, err := s.Adapter.Exec(ctx, rehyd, []string{"sh", "-c", "echo ok"})
	if err != nil {
		r.Fatalf("Exec on rehydrated instance: %v", err)
	}
	if res.ExitCode != 0 || !bytes.Contains(res.Stdout, []byte("ok")) {
		r.Fatalf("Exec on rehydrated: exit=%d stdout=%q", res.ExitCode, res.Stdout)
	}
}

func caseDestroyIdempotent(r Reporter, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(r))
	if err != nil {
		r.Fatalf("Create: %v", err)
	}
	if err := s.Adapter.Destroy(ctx, inst); err != nil {
		r.Fatalf("first Destroy: %v", err)
	}
	if err := s.Adapter.Destroy(ctx, inst); err != nil {
		r.Fatalf("second Destroy (idempotence): %v", err)
	}
}

func caseEventsPump(r Reporter, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(r))
	if err != nil {
		r.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()

	evCh, err := s.Adapter.Events(ctx, inst)
	if err != nil {
		r.Fatalf("Events: %v", err)
	}

	if s.ForceLifecycleEvent != nil {
		s.ForceLifecycleEvent(r, inst)
	}

	select {
	case evt, ok := <-evCh:
		if !ok {
			r.Fatalf("Events channel closed before any event arrived")
		}
		if evt.Kind == "" {
			r.Fatalf("event with empty Kind")
		}
	case <-time.After(2 * time.Second):
		if s.ForceLifecycleEvent != nil {
			r.Fatalf("no event received within 2s after ForceLifecycleEvent")
		}
	}
}

func caseEventsRespawn(r Reporter, s Suite) {
	if !s.SupportsEventsRespawn {
		r.Skip("adapter does not have a killable events producer")
		return
	}
	// Detailed respawn behavior is exercised in adapter-specific tests with
	// tunable heartbeat/respawn timings (see internal/env/command/events_test.go).
	// The conformance gate here is just a smoke test: Events must emit at
	// least one event and stay open during the window.
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(r))
	if err != nil {
		r.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Adapter.Destroy(ctx, inst) }()
	evCh, err := s.Adapter.Events(ctx, inst)
	if err != nil {
		r.Fatalf("Events: %v", err)
	}
	select {
	case evt, ok := <-evCh:
		if !ok {
			r.Fatalf("Events channel closed before any event arrived")
		}
		if evt.Kind == "" {
			r.Fatalf("event with empty Kind")
		}
	case <-time.After(3 * time.Second):
		r.Fatalf("no event observed within 3s on a respawn-capable adapter")
	}
}

func caseStatusLifecycle(r Reporter, s Suite) {
	ctx, cancel := testCtx()
	defer cancel()
	inst, err := s.Adapter.Create(ctx, s.NewSpec(r))
	if err != nil {
		r.Fatalf("Create: %v", err)
	}
	st, err := s.Adapter.Status(ctx, inst)
	if err != nil {
		r.Fatalf("Status: %v", err)
	}
	if !st.Running {
		r.Fatalf("Status.Running=false after Create; want true")
	}
	if err := s.Adapter.Destroy(ctx, inst); err != nil {
		r.Fatalf("Destroy: %v", err)
	}
	if st2, err := s.Adapter.Status(ctx, inst); err == nil && st2.Running {
		r.Fatalf("Status.Running=true after Destroy; want false or error")
	}
}

// testCtx returns a context bounded at 30s — enough for all current cases.
func testCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// readWithTimeout reads from r until EOF or timeout. Returns whatever was
// read.
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

// CLIReporter is a Reporter implementation for standalone (non-go-test) runs.
// It buffers logs per-case and records the pass/fail/skip outcome.
type CLIReporter struct {
	Logs    strings.Builder
	failed  bool
	skipped bool
}

func (c *CLIReporter) Helper() {}
func (c *CLIReporter) Fatalf(format string, args ...any) {
	c.failed = true
	fmt.Fprintf(&c.Logs, format+"\n", args...)
	panic(&cliAbort{})
}
func (c *CLIReporter) Logf(format string, args ...any) {
	fmt.Fprintf(&c.Logs, format+"\n", args...)
}
func (c *CLIReporter) Skip(args ...any) {
	c.skipped = true
	fmt.Fprintln(&c.Logs, args...)
	panic(&cliAbort{})
}
func (c *CLIReporter) Failed() bool  { return c.failed }
func (c *CLIReporter) Skipped() bool { return c.skipped }

type cliAbort struct{}

// RunOne runs a single case with a CLIReporter. Panics from Fatalf/Skip are
// recovered so the runner can move on to the next case.
func RunOne(fn CaseFunc, s Suite) *CLIReporter {
	rep := &CLIReporter{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(*cliAbort); ok {
					return
				}
				panic(r)
			}
		}()
		fn(rep, s)
	}()
	return rep
}
