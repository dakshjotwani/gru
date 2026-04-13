# Gru Phase 1c — Session Control & CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the `SessionController` abstraction, Claude Code tmux-based launcher, process liveness supervisor, real gRPC `LaunchSession`/`KillSession` handlers, and a `gru` CLI with `status`, `kill`, `launch`, `tail`, and `attach` commands.

**Architecture:** A `SessionController` interface abstracts runtime-specific process management; the Claude Code implementation uses tmux — one tmux session per project, one tmux window per agent — for process isolation and attach support. A `Supervisor` goroutine polls running sessions every 10 seconds and marks crashed windows as errored. The `gru` CLI is a thin gRPC client that connects to the local server using the API key from `~/.gru/server.yaml`.

**Tech Stack:** Go 1.23+, `connectrpc.com/connect`, `os/exec`, `tmux`, `net/http/httptest`, `fmt.Fprintf` for table output.

---

## File Map

```
internal/controller/controller.go              # SessionController interface + ControllerRegistry
internal/controller/controller_test.go
internal/controller/claude/controller.go       # Claude Code SessionController (tmux-based)
internal/controller/claude/controller_test.go  # uses a fake tmuxRunner
internal/supervisor/supervisor.go              # process liveness poller
internal/supervisor/supervisor_test.go
internal/server/service.go                     # MODIFIED: implement LaunchSession + KillSession
internal/server/service_test.go                # MODIFIED: add launch/kill tests
cmd/gru/root.go                                # root cobra command, persistent flags, shared state
cmd/gru/cmd_status.go                          # `gru status [id]`
cmd/gru/cmd_kill.go                            # `gru kill <id>`
cmd/gru/cmd_launch.go                          # `gru launch <dir> <prompt> [--profile]`
cmd/gru/cmd_tail.go                            # `gru tail <id>`
cmd/gru/cmd_attach.go                          # `gru attach <id-or-project>`
cmd/gru/root_test.go                           # CLI tests using cobra SetArgs + SetOut
cmd/gru/main.go                                # MODIFIED: delegates to newRootCmd().Execute()
cmd/gru/server.go                              # MODIFIED: wraps runServer() in newServerCmd()
```

---

### Task 1: SessionController Interface and ControllerRegistry

**Files:**
- Create: `internal/controller/controller.go`
- Create: `internal/controller/controller_test.go`

- [ ] **Step 1: Write the failing test first**

Create `internal/controller/controller_test.go`:

```go
package controller_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dakshjotwani/gru/internal/controller"
)

// fakeController implements SessionController for testing ControllerRegistry.
type fakeController struct {
	runtimeID    string
	capabilities []controller.Capability
}

func (f *fakeController) RuntimeID() string                    { return f.runtimeID }
func (f *fakeController) Capabilities() []controller.Capability { return f.capabilities }
func (f *fakeController) Launch(_ context.Context, _ controller.LaunchOptions) (*controller.SessionHandle, error) {
	return nil, errors.New("fake: not implemented")
}

func TestControllerRegistry_RegisterAndGet(t *testing.T) {
	reg := controller.NewRegistry()
	fc := &fakeController{runtimeID: "fake-runtime", capabilities: []controller.Capability{controller.CapKill}}
	reg.Register(fc)

	got, err := reg.Get("fake-runtime")
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.RuntimeID() != "fake-runtime" {
		t.Errorf("RuntimeID = %q, want %q", got.RuntimeID(), "fake-runtime")
	}
}

func TestControllerRegistry_GetUnknown(t *testing.T) {
	reg := controller.NewRegistry()
	_, err := reg.Get("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown runtime, got nil")
	}
}

func TestControllerRegistry_RegisterDuplicate(t *testing.T) {
	reg := controller.NewRegistry()
	fc := &fakeController{runtimeID: "dup"}
	reg.Register(fc)
	// registering a second time should panic (programming error, caught early)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate Register, got none")
		}
	}()
	reg.Register(fc)
}
```

- [ ] **Step 2: Run the test — confirm it fails to compile**

```bash
cd /path/to/gru && go test ./internal/controller/...
```

Expected: compile error — package `controller` does not exist yet.

- [ ] **Step 3: Create `internal/controller/controller.go`**

```go
package controller

import (
	"context"
	"fmt"
)

// Capability names an operation a SessionController supports.
type Capability string

const (
	CapKill          Capability = "kill"
	CapPause         Capability = "pause"
	CapResume        Capability = "resume"
	CapInjectContext Capability = "inject_context"
)

// LaunchOptions carries everything needed to start a new agent session.
type LaunchOptions struct {
	SessionID  string            // pre-generated by server; generated by controller if empty
	ProjectDir string
	Prompt     string
	Profile    string
	Env        map[string]string // additional env vars beyond the GRU_* ones
}

// SessionHandle is returned by Launch and gives callers control over a running agent.
type SessionHandle struct {
	SessionID   string
	TmuxSession string // e.g. "gru-av-sim"
	TmuxWindow  string // e.g. "feat-dev·a1b2c3d4"
	// Kill terminates the tmux window.
	Kill func(ctx context.Context) error
	// Done is closed when the tmux window disappears.
	Done <-chan struct{}
	// ExitCode returns 0 for tmux-managed sessions (exit code not available via tmux).
	ExitCode func() int
}

// SessionController abstracts runtime-specific process management.
type SessionController interface {
	RuntimeID() string
	Capabilities() []Capability
	Launch(ctx context.Context, opts LaunchOptions) (*SessionHandle, error)
}

// Registry holds one SessionController per runtime ID.
type Registry struct {
	controllers map[string]SessionController
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{controllers: make(map[string]SessionController)}
}

// Register adds c to the registry. Panics if a controller with the same
// RuntimeID has already been registered (programming error).
func (r *Registry) Register(c SessionController) {
	id := c.RuntimeID()
	if _, exists := r.controllers[id]; exists {
		panic(fmt.Sprintf("controller: duplicate registration for runtime %q", id))
	}
	r.controllers[id] = c
}

// Get returns the controller for the given runtime ID, or an error if not found.
func (r *Registry) Get(runtimeID string) (SessionController, error) {
	c, ok := r.controllers[runtimeID]
	if !ok {
		return nil, fmt.Errorf("controller: no controller registered for runtime %q", runtimeID)
	}
	return c, nil
}
```

- [ ] **Step 4: Run the tests — confirm they pass**

```bash
go test ./internal/controller/...
```

Expected:
```
ok  	github.com/dakshjotwani/gru/internal/controller	0.XXXs
```

- [ ] **Step 5: Commit**

```bash
git add internal/controller/controller.go internal/controller/controller_test.go
git commit -m "feat: add SessionController interface and ControllerRegistry"
```

---

### Task 2: Claude Code SessionController

**Files:**
- Create: `internal/controller/claude/controller.go`
- Create: `internal/controller/claude/controller_test.go`

- [ ] **Step 1: Write the failing tests first**

Create `internal/controller/claude/controller_test.go`:

```go
package claude_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dakshjotwani/gru/internal/controller"
	claudectrl "github.com/dakshjotwani/gru/internal/controller/claude"
)

// fakeTmux records tmux calls for verification.
type fakeTmux struct {
	runs    [][]string
	outputs map[string][]byte
	errs    map[string]error
}

func (f *fakeTmux) Run(args ...string) error {
	f.runs = append(f.runs, args)
	key := strings.Join(args[:1], " ")
	if err, ok := f.errs[key]; ok {
		return err
	}
	return nil
}

func (f *fakeTmux) Output(args ...string) ([]byte, error) {
	f.runs = append(f.runs, args)
	key := strings.Join(args, " ")
	if out, ok := f.outputs[key]; ok {
		return out, nil
	}
	return nil, nil
}

func newFakeTmux() *fakeTmux {
	return &fakeTmux{outputs: make(map[string][]byte), errs: make(map[string]error)}
}

func TestClaudeController_RuntimeID(t *testing.T) {
	c := claudectrl.NewClaudeController("key", "localhost", "7070")
	if got := c.RuntimeID(); got != "claude-code" {
		t.Errorf("RuntimeID = %q, want %q", got, "claude-code")
	}
}

func TestClaudeController_Capabilities(t *testing.T) {
	c := claudectrl.NewClaudeController("key", "localhost", "7070")
	caps := c.Capabilities()
	if len(caps) != 1 || caps[0] != controller.CapKill {
		t.Errorf("Capabilities = %v, want [kill]", caps)
	}
}

func TestClaudeController_Launch_SessionAndWindowCreated(t *testing.T) {
	ft := newFakeTmux()
	c := claudectrl.NewClaudeControllerWithRunner("key", "localhost", "7070", ft)
	projectDir := t.TempDir()

	handle, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID:  "abcd1234-0000-0000-0000-000000000001",
		ProjectDir: projectDir,
		Prompt:     "hello world",
		Profile:    "feat-dev",
	})
	if err != nil {
		t.Fatalf("Launch: unexpected error: %v", err)
	}
	if handle.TmuxSession == "" {
		t.Error("TmuxSession is empty")
	}
	if handle.TmuxWindow == "" {
		t.Error("TmuxWindow is empty")
	}
	if handle.Done == nil {
		t.Error("Done channel is nil")
	}
	if handle.Kill == nil {
		t.Error("Kill func is nil")
	}

	// Verify new-session was called
	var foundSession bool
	for _, call := range ft.runs {
		if len(call) > 0 && call[0] == "new-session" {
			foundSession = true
		}
	}
	if !foundSession {
		t.Error("tmux new-session was not called")
	}

	// Verify new-window was called with correct env vars
	var foundWindow bool
	for _, call := range ft.runs {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "new-window") && strings.Contains(joined, "GRU_SESSION_ID") {
			foundWindow = true
		}
	}
	if !foundWindow {
		t.Error("tmux new-window with GRU_SESSION_ID was not called")
	}
}

func TestClaudeController_Launch_WindowNameFormat(t *testing.T) {
	ft := newFakeTmux()
	c := claudectrl.NewClaudeControllerWithRunner("key", "localhost", "7070", ft)
	projectDir := t.TempDir()

	handle, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID:  "abcd1234-0000-0000-0000-000000000001",
		ProjectDir: projectDir,
		Prompt:     "test",
		Profile:    "feat-dev",
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// Window name should be "feat-dev·<short-id>"
	if !strings.HasPrefix(handle.TmuxWindow, "feat-dev·") {
		t.Errorf("TmuxWindow = %q, want prefix %q", handle.TmuxWindow, "feat-dev·")
	}
	if !strings.Contains(handle.TmuxWindow, "abcd1234") {
		t.Errorf("TmuxWindow = %q, want short ID %q", handle.TmuxWindow, "abcd1234")
	}
}

func TestClaudeController_Launch_Kill(t *testing.T) {
	ft := newFakeTmux()
	// Simulate window gone after kill
	c := claudectrl.NewClaudeControllerWithRunner("key", "localhost", "7070", ft)
	projectDir := t.TempDir()

	handle, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID:  "abcd1234-0000-0000-0000-000000000001",
		ProjectDir: projectDir,
		Prompt:     "long running",
		Profile:    "feat-dev",
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	if err := handle.Kill(context.Background()); err != nil {
		t.Fatalf("Kill: unexpected error: %v", err)
	}

	// Verify kill-window was called
	var foundKill bool
	for _, call := range ft.runs {
		if len(call) > 0 && call[0] == "kill-window" {
			foundKill = true
		}
	}
	if !foundKill {
		t.Error("tmux kill-window was not called")
	}
}
```

- [ ] **Step 2: Run the test — confirm compile failure**

```bash
go test ./internal/controller/claude/...
```

Expected: compile error — package `claude` does not exist yet.

- [ ] **Step 3: Create `internal/controller/claude/controller.go`**

```go
package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/dakshjotwani/gru/internal/controller"
)

// tmuxRunner abstracts tmux command execution for testability.
type tmuxRunner interface {
	Run(args ...string) error
	Output(args ...string) ([]byte, error)
}

// realTmux executes actual tmux commands.
type realTmux struct{}

func (r *realTmux) Run(args ...string) error {
	return exec.Command("tmux", args...).Run()
}

func (r *realTmux) Output(args ...string) ([]byte, error) {
	return exec.Command("tmux", args...).Output()
}

// ClaudeController is a SessionController for Claude Code agents using tmux.
type ClaudeController struct {
	apiKey string
	host   string
	port   string
	tmux   tmuxRunner
}

// NewClaudeController returns a new Claude Code SessionController.
func NewClaudeController(apiKey, host, port string) *ClaudeController {
	return &ClaudeController{apiKey: apiKey, host: host, port: port, tmux: &realTmux{}}
}

// NewClaudeControllerWithRunner returns a controller with a custom tmux runner (for tests).
func NewClaudeControllerWithRunner(apiKey, host, port string, runner tmuxRunner) *ClaudeController {
	return &ClaudeController{apiKey: apiKey, host: host, port: port, tmux: runner}
}

// RuntimeID implements SessionController.
func (c *ClaudeController) RuntimeID() string { return "claude-code" }

// Capabilities implements SessionController.
func (c *ClaudeController) Capabilities() []controller.Capability {
	return []controller.Capability{controller.CapKill}
}

// sanitizeProjectName lowercases and replaces /, spaces, dots with -.
// Strips a leading "gru-" prefix if already present.
func sanitizeProjectName(name string) string {
	name = strings.ToLower(name)
	replacer := strings.NewReplacer("/", "-", " ", "-", ".", "-")
	name = replacer.Replace(name)
	name = strings.TrimPrefix(name, "gru-")
	return name
}

// Launch implements SessionController. It starts `claude --dangerously-skip-permissions -p "<prompt>"`
// inside a tmux window, injecting GRU_* env vars.
func (c *ClaudeController) Launch(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
	if _, err := os.Stat(opts.ProjectDir); err != nil {
		return nil, fmt.Errorf("claude: project dir: %w", err)
	}

	// Determine session ID.
	sessionID := opts.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	// Derive tmux session name from project dir base name.
	projectName := sanitizeProjectName(opts.ProjectDir)
	tmuxSession := "gru-" + projectName

	// Ensure the tmux session exists (ignore "duplicate session" errors).
	if err := c.tmux.Run("new-session", "-d", "-s", tmuxSession); err != nil {
		// exit code 1 with "duplicate session" is OK
		exitErr, ok := err.(*exec.ExitError)
		if !ok || !strings.Contains(string(exitErr.Stderr), "duplicate session") {
			// non-duplicate error: ignore silently (tmux may not be running yet; window creation will fail below)
			_ = err
		}
	}

	// Build window name.
	shortID := sessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	var windowName string
	if opts.Profile != "" {
		windowName = opts.Profile + "·" + shortID
	} else {
		windowName = shortID
	}

	// Build the claude command string.
	claudeCmd := fmt.Sprintf("claude --dangerously-skip-permissions -p '%s'", opts.Prompt)

	// Create the tmux window.
	newWindowArgs := []string{
		"new-window",
		"-t", tmuxSession,
		"-n", windowName,
		"-c", opts.ProjectDir,
		"-e", "GRU_SESSION_ID=" + sessionID,
		"-e", "GRU_API_KEY=" + c.apiKey,
		"-e", "GRU_HOST=" + c.host,
		"-e", "GRU_PORT=" + c.port,
		claudeCmd,
	}
	if err := c.tmux.Run(newWindowArgs...); err != nil {
		return nil, fmt.Errorf("claude: tmux new-window: %w", err)
	}

	done := make(chan struct{})

	// Background goroutine: poll until the window disappears.
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				out, err := c.tmux.Output("list-windows", "-t", tmuxSession, "-F", "#{window_name}")
				if err != nil {
					// Session gone entirely — treat as done.
					return
				}
				if !strings.Contains(string(out), windowName) {
					return
				}
			}
		}
	}()

	killFn := func(killCtx context.Context) error {
		target := tmuxSession + ":" + windowName
		if err := c.tmux.Run("kill-window", "-t", target); err != nil {
			return fmt.Errorf("claude: kill-window %s: %w", target, err)
		}
		return nil
	}

	return &controller.SessionHandle{
		SessionID:   sessionID,
		TmuxSession: tmuxSession,
		TmuxWindow:  windowName,
		Kill:        killFn,
		Done:        done,
		ExitCode:    func() int { return 0 },
	}, nil
}
```

- [ ] **Step 4: Run the tests — confirm they pass**

```bash
go test ./internal/controller/...
```

Expected:
```
ok  	github.com/dakshjotwani/gru/internal/controller	0.XXXs
ok  	github.com/dakshjotwani/gru/internal/controller/claude	0.XXXs
```

- [ ] **Step 5: Commit**

```bash
git add internal/controller/claude/
git commit -m "feat: add Claude Code SessionController (tmux-based)"
```

---

### Task 3: Process Liveness Supervisor

**Files:**
- Create: `internal/supervisor/supervisor.go`
- Create: `internal/supervisor/supervisor_test.go`

- [ ] **Step 1: Write the failing test first**

Create `internal/supervisor/supervisor_test.go`:

```go
package supervisor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/supervisor"
)

// fakeSessionStore satisfies supervisor.SessionStore for testing.
type fakeSessionStore struct {
	sessions []supervisor.LiveSession
	updated  []supervisor.StatusUpdate
}

func (f *fakeSessionStore) ListLiveSessions(ctx context.Context) ([]supervisor.LiveSession, error) {
	return f.sessions, nil
}

func (f *fakeSessionStore) MarkSessionErrored(ctx context.Context, sessionID string) error {
	f.updated = append(f.updated, supervisor.StatusUpdate{SessionID: sessionID, Status: "errored"})
	return nil
}

// fakePublisher satisfies supervisor.EventPublisher for testing.
type fakePublisher struct {
	events []supervisor.CrashEvent
}

func (f *fakePublisher) PublishCrash(ctx context.Context, e supervisor.CrashEvent) {
	f.events = append(f.events, e)
}

// fakeTmuxRunner simulates tmux list-windows output.
type fakeTmuxRunner struct {
	// windowsBySession maps tmux session name → list of window names that exist
	windowsBySession map[string][]string
}

func (f *fakeTmuxRunner) Output(args ...string) ([]byte, error) {
	// args: ["list-windows", "-t", <session>, "-F", "#{window_name}"]
	if len(args) >= 3 && args[0] == "list-windows" {
		session := args[2]
		windows := f.windowsBySession[session]
		return []byte(strings.Join(windows, "\n") + "\n"), nil
	}
	return nil, nil
}

func TestSupervisor_MarksDeadWindowErrored(t *testing.T) {
	// Simulate a tmux session where the window no longer exists.
	tmux := &fakeTmuxRunner{
		windowsBySession: map[string][]string{
			"gru-av-sim": {}, // window "feat-dev·abcd1234" is gone
		},
	}

	store := &fakeSessionStore{
		sessions: []supervisor.LiveSession{
			{
				ID:          "sess-dead",
				TmuxSession: "gru-av-sim",
				TmuxWindow:  "feat-dev·abcd1234",
				Status:      "running",
			},
		},
	}
	pub := &fakePublisher{}

	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sv.ReconcileOnce(ctx)

	if len(store.updated) != 1 {
		t.Fatalf("expected 1 status update, got %d", len(store.updated))
	}
	if store.updated[0].SessionID != "sess-dead" {
		t.Errorf("updated session ID = %q, want %q", store.updated[0].SessionID, "sess-dead")
	}
	if store.updated[0].Status != "errored" {
		t.Errorf("updated status = %q, want %q", store.updated[0].Status, "errored")
	}
	if len(pub.events) != 1 || pub.events[0].SessionID != "sess-dead" {
		t.Errorf("expected 1 crash event for sess-dead, got %v", pub.events)
	}
}

func TestSupervisor_DoesNotMarkAliveWindow(t *testing.T) {
	// Simulate a tmux session where the window still exists.
	tmux := &fakeTmuxRunner{
		windowsBySession: map[string][]string{
			"gru-av-sim": {"feat-dev·abcd1234"},
		},
	}

	store := &fakeSessionStore{
		sessions: []supervisor.LiveSession{
			{
				ID:          "sess-alive",
				TmuxSession: "gru-av-sim",
				TmuxWindow:  "feat-dev·abcd1234",
				Status:      "running",
			},
		},
	}
	pub := &fakePublisher{}

	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sv.ReconcileOnce(ctx)

	if len(store.updated) != 0 {
		t.Errorf("expected no updates for alive window, got %v", store.updated)
	}
	if len(pub.events) != 0 {
		t.Errorf("expected no crash events for alive window, got %v", pub.events)
	}
}

func TestSupervisor_RunPollsRepeatedly(t *testing.T) {
	// Simulate a window that is gone from the start.
	tmux := &fakeTmuxRunner{
		windowsBySession: map[string][]string{
			"gru-myproject": {}, // empty — window is gone
		},
	}

	store := &fakeSessionStore{
		sessions: []supervisor.LiveSession{
			{
				ID:          "sess-poll",
				TmuxSession: "gru-myproject",
				TmuxWindow:  "feat-dev·poll1234",
				Status:      "starting",
			},
		},
	}
	pub := &fakePublisher{}

	sv := supervisor.NewWithRunner(store, pub, 50*time.Millisecond, tmux)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	sv.Run(ctx)

	// After multiple ticks, MarkSessionErrored should have been called at least once.
	if len(store.updated) == 0 {
		t.Fatal("expected at least one status update after supervisor run")
	}
}
```

- [ ] **Step 2: Run the test — confirm it fails to compile**

```bash
go test ./internal/supervisor/...
```

Expected: compile error — package `supervisor` does not exist.

- [ ] **Step 3: Create `internal/supervisor/supervisor.go`**

```go
package supervisor

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// LiveSession represents a session that should have a running tmux window.
type LiveSession struct {
	ID          string
	TmuxSession string // e.g. "gru-av-sim"
	TmuxWindow  string // e.g. "feat-dev·a1b2c3d4"
	Status      string // "starting" or "running"
}

// StatusUpdate records what the supervisor changed.
type StatusUpdate struct {
	SessionID string
	Status    string
}

// CrashEvent is published when the supervisor detects a dead window.
type CrashEvent struct {
	SessionID   string
	TmuxSession string
	TmuxWindow  string
}

// SessionStore is the storage interface used by the Supervisor.
type SessionStore interface {
	// ListLiveSessions returns all sessions with status "starting" or "running".
	ListLiveSessions(ctx context.Context) ([]LiveSession, error)
	// MarkSessionErrored sets the session status to "errored".
	MarkSessionErrored(ctx context.Context, sessionID string) error
}

// EventPublisher emits crash events to subscribers.
type EventPublisher interface {
	PublishCrash(ctx context.Context, e CrashEvent)
}

// tmuxOutputRunner is the minimal interface needed to check window liveness.
type tmuxOutputRunner interface {
	Output(args ...string) ([]byte, error)
}

// realTmuxRunner runs actual tmux commands.
type realTmuxRunner struct{}

func (r *realTmuxRunner) Output(args ...string) ([]byte, error) {
	return exec.Command("tmux", args...).Output()
}

// Supervisor polls running sessions for tmux window liveness.
type Supervisor struct {
	store    SessionStore
	pub      EventPublisher
	interval time.Duration
	tmux     tmuxOutputRunner
}

// New returns a Supervisor that polls every interval using real tmux.
func New(store SessionStore, pub EventPublisher, interval time.Duration) *Supervisor {
	return &Supervisor{store: store, pub: pub, interval: interval, tmux: &realTmuxRunner{}}
}

// NewWithRunner returns a Supervisor with an injected tmux runner (for tests).
func NewWithRunner(store SessionStore, pub EventPublisher, interval time.Duration, tmux tmuxOutputRunner) *Supervisor {
	return &Supervisor{store: store, pub: pub, interval: interval, tmux: tmux}
}

// windowExists checks whether tmuxWindow is present in the tmux session's window list.
func (s *Supervisor) windowExists(tmuxSession, tmuxWindow string) bool {
	out, err := s.tmux.Output("list-windows", "-t", tmuxSession, "-F", "#{window_name}")
	if err != nil {
		// Session gone entirely — treat as dead.
		return false
	}
	return strings.Contains(string(out), tmuxWindow)
}

// ReconcileOnce checks all live sessions exactly once and marks dead ones errored.
func (s *Supervisor) ReconcileOnce(ctx context.Context) {
	sessions, err := s.store.ListLiveSessions(ctx)
	if err != nil {
		return
	}
	for _, sess := range sessions {
		if sess.TmuxSession == "" || sess.TmuxWindow == "" {
			continue // no tmux info yet — skip
		}
		if s.windowExists(sess.TmuxSession, sess.TmuxWindow) {
			continue // window still alive
		}
		// Window is gone but session is still marked live.
		if err := s.store.MarkSessionErrored(ctx, sess.ID); err != nil {
			continue
		}
		s.pub.PublishCrash(ctx, CrashEvent{
			SessionID:   sess.ID,
			TmuxSession: sess.TmuxSession,
			TmuxWindow:  sess.TmuxWindow,
		})
	}
}

// Run starts the polling loop and blocks until ctx is cancelled.
// Call ReconcileOnce first for startup reconciliation, then poll on interval.
func (s *Supervisor) Run(ctx context.Context) {
	s.ReconcileOnce(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ReconcileOnce(ctx)
		}
	}
}
```

- [ ] **Step 4: Run the tests — confirm they pass**

```bash
go test ./internal/supervisor/...
```

Expected:
```
ok  	github.com/dakshjotwani/gru/internal/supervisor	0.XXXs
```

- [ ] **Step 5: Commit**

```bash
git add internal/supervisor/
git commit -m "feat: add process liveness supervisor with ReconcileOnce and Run"
```

---

### Task 4: Implement LaunchSession and KillSession in gRPC Service

**Files:**
- Modify: `internal/server/service.go`
- Modify: `internal/server/service_test.go`

- [ ] **Step 1: Read the current service.go**

Before editing, read the existing stub to understand the current Service struct and constructor:

```bash
cat internal/server/service.go
```

Expected to see a `Service` struct with `Queries()`, `Publisher()`, and stub `LaunchSession`/`KillSession` returning `CodeUnimplemented`.

- [ ] **Step 2: Write the new failing tests first**

Append to (or replace the launch/kill section of) `internal/server/service_test.go`:

```go
// --- LaunchSession / KillSession tests ---

func TestService_LaunchSession(t *testing.T) {
	svc, store := newTestService(t) // helper from existing tests

	// Register a fake controller via the registry.
	reg := controller.NewRegistry()
	launched := make(chan controller.LaunchOptions, 1)
	reg.Register(&fakeSessionController{
		runtimeID: "claude-code",
		launchFn: func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
			launched <- opts
			done := make(chan struct{})
			close(done) // exits immediately
			return &controller.SessionHandle{
				SessionID:   opts.SessionID,
				TmuxSession: "gru-testproject",
				TmuxWindow:  "feat-dev·abcd1234",
				Kill:        func(ctx context.Context) error { return nil },
				Done:        done,
				ExitCode:    func() int { return 0 },
			}, nil
		},
	})
	svc.SetControllerRegistry(reg)

	// Upsert a project so LaunchSession can find/create it.
	projectDir := t.TempDir()

	req := connect.NewRequest(&gruv1.LaunchSessionRequest{
		ProjectDir: projectDir,
		Prompt:     "write tests",
		Profile:    "default",
	})
	req.Header().Set("Authorization", "Bearer "+testAPIKey)

	resp, err := svc.LaunchSession(context.Background(), req)
	if err != nil {
		t.Fatalf("LaunchSession: unexpected error: %v", err)
	}
	if resp.Msg.Session == nil {
		t.Fatal("expected Session in response, got nil")
	}
	sess := resp.Msg.Session
	if sess.Id == "" {
		t.Error("session ID is empty")
	}
	if sess.Status != gruv1.SessionStatus_SESSION_STATUS_STARTING {
		t.Errorf("Status = %v, want STARTING", sess.Status)
	}
	if sess.Runtime != "claude-code" {
		t.Errorf("Runtime = %q, want claude-code", sess.Runtime)
	}
	if sess.TmuxSession != "gru-testproject" {
		t.Errorf("TmuxSession = %q, want %q", sess.TmuxSession, "gru-testproject")
	}
	if sess.TmuxWindow != "feat-dev·abcd1234" {
		t.Errorf("TmuxWindow = %q, want %q", sess.TmuxWindow, "feat-dev·abcd1234")
	}

	// Verify the launch options were forwarded.
	select {
	case opts := <-launched:
		if opts.Prompt != "write tests" {
			t.Errorf("Prompt = %q, want %q", opts.Prompt, "write tests")
		}
		if opts.SessionID == "" {
			t.Error("SessionID was not set in LaunchOptions")
		}
	default:
		t.Error("controller.Launch was not called")
	}

	// Verify the row is in the DB with tmux fields.
	stored, err := store.GetSession(context.Background(), sess.Id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if stored.TmuxSession == nil || *stored.TmuxSession != "gru-testproject" {
		t.Errorf("stored TmuxSession = %v, want gru-testproject", stored.TmuxSession)
	}
}

func TestService_KillSession(t *testing.T) {
	svc, store := newTestService(t)

	reg := controller.NewRegistry()
	killCalled := make(chan struct{}, 1)
	reg.Register(&fakeSessionController{
		runtimeID: "claude-code",
		launchFn: func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
			done := make(chan struct{})
			close(done)
			return &controller.SessionHandle{
				SessionID:   opts.SessionID,
				TmuxSession: "gru-testproject",
				TmuxWindow:  "feat-dev·kill1234",
				Kill:        func(ctx context.Context) error { killCalled <- struct{}{}; return nil },
				Done:        done,
				ExitCode:    func() int { return 0 },
			}, nil
		},
	})
	svc.SetControllerRegistry(reg)

	projectDir := t.TempDir()

	// Launch a session first.
	launchReq := connect.NewRequest(&gruv1.LaunchSessionRequest{
		ProjectDir: projectDir,
		Prompt:     "do work",
	})
	launchReq.Header().Set("Authorization", "Bearer "+testAPIKey)

	launchResp, err := svc.LaunchSession(context.Background(), launchReq)
	if err != nil {
		t.Fatalf("LaunchSession: %v", err)
	}
	sessionID := launchResp.Msg.Session.Id

	// Now kill it.
	killReq := connect.NewRequest(&gruv1.KillSessionRequest{Id: sessionID})
	killReq.Header().Set("Authorization", "Bearer "+testAPIKey)

	killResp, err := svc.KillSession(context.Background(), killReq)
	if err != nil {
		t.Fatalf("KillSession: unexpected error: %v", err)
	}
	if !killResp.Msg.Success {
		t.Error("KillSession: Success = false, want true")
	}

	// Verify kill func was invoked.
	select {
	case <-killCalled:
		// good
	default:
		t.Error("handle.Kill was not called")
	}

	// Verify status updated in DB.
	stored, err := store.GetSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetSession after kill: %v", err)
	}
	if stored.Status != "killed" {
		t.Errorf("status after kill = %q, want %q", stored.Status, "killed")
	}
}

func TestService_KillSession_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	svc.SetControllerRegistry(controller.NewRegistry())

	req := connect.NewRequest(&gruv1.KillSessionRequest{Id: "nonexistent-id"})
	req.Header().Set("Authorization", "Bearer "+testAPIKey)

	_, err := svc.KillSession(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonexistent session, got nil")
	}
}

// fakeSessionController implements controller.SessionController for tests.
type fakeSessionController struct {
	runtimeID string
	launchFn  func(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error)
}

func (f *fakeSessionController) RuntimeID() string                    { return f.runtimeID }
func (f *fakeSessionController) Capabilities() []controller.Capability { return nil }
func (f *fakeSessionController) Launch(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
	return f.launchFn(ctx, opts)
}
```

- [ ] **Step 3: Run the new tests — confirm they fail**

```bash
go test ./internal/server/... -run TestService_LaunchSession
```

Expected: compile or runtime failure — `SetControllerRegistry` and handle tracking not yet implemented.

- [ ] **Step 4: Update `internal/server/service.go`**

Add the `ControllerRegistry` field, `SetControllerRegistry` setter, handle map, and real implementations. The complete updated file (keeping all existing fields and stubs):

```go
package server

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/dakshjotwani/gru/internal/controller"
	"github.com/dakshjotwani/gru/internal/store"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
)

// Ensure Service satisfies the generated interface.
var _ gruv1connect.GruServiceHandler = (*Service)(nil)

// Service implements the GruService gRPC/connect handler.
type Service struct {
	cfg        Config
	store      *store.Store
	publisher  Publisher

	// controllerReg holds SessionControllers indexed by runtime ID.
	controllerReg *controller.Registry

	// handlesMu guards handles map (live session handles).
	handlesMu sync.Mutex
	handles   map[string]*controller.SessionHandle // key: session ID
}

// Config is the subset of server config the Service needs.
type Config struct {
	Addr   string
	APIKey string
}

// Publisher is the event broadcast interface (set by server wiring).
type Publisher interface {
	Publish(event store.GruEvent)
}

// NewService constructs a Service. Call SetControllerRegistry before serving.
func NewService(cfg Config, st *store.Store, pub Publisher) *Service {
	return &Service{
		cfg:           cfg,
		store:         st,
		publisher:     pub,
		controllerReg: controller.NewRegistry(),
		handles:       make(map[string]*controller.SessionHandle),
	}
}

// SetControllerRegistry replaces the controller registry (useful in tests).
func (s *Service) SetControllerRegistry(reg *controller.Registry) {
	s.controllerReg = reg
}

// Queries returns the sqlc query interface.
func (s *Service) Queries() store.Querier {
	return s.store.Queries()
}

// --- LaunchSession ---

func (s *Service) LaunchSession(
	ctx context.Context,
	req *connect.Request[gruv1.LaunchSessionRequest],
) (*connect.Response[gruv1.LaunchSessionResponse], error) {
	projectDir := filepath.Clean(req.Msg.ProjectDir)
	prompt := req.Msg.Prompt
	profile := req.Msg.Profile

	// Resolve or create the project row.
	projectID, err := s.upsertProject(ctx, projectDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("upsert project: %w", err))
	}

	// Pick the controller — default to claude-code.
	runtimeID := "claude-code"
	ctrl, err := s.controllerReg.Get(runtimeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no controller for runtime %q", runtimeID))
	}

	sessionID := uuid.NewString()

	// Create the DB row (status = starting) before launching so we always have a record.
	_, err = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID:        sessionID,
		ProjectID: projectID,
		Runtime:   runtimeID,
		Status:    "starting",
		Profile:   nilString(profile),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session row: %w", err))
	}

	handle, err := ctrl.Launch(ctx, controller.LaunchOptions{
		SessionID:  sessionID,
		ProjectDir: projectDir,
		Prompt:     prompt,
		Profile:    profile,
	})
	if err != nil {
		// Mark session as errored if launch fails.
		_, _ = s.Queries().UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
			Status: "errored",
			ID:     sessionID,
		})
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("launch: %w", err))
	}

	// Persist tmux session and window info.
	_ = s.Queries().UpdateSessionTmux(ctx, store.UpdateSessionTmuxParams{
		TmuxSession: nilString(handle.TmuxSession),
		TmuxWindow:  nilString(handle.TmuxWindow),
		ID:          sessionID,
	})

	// Store the handle so KillSession can find it.
	s.handlesMu.Lock()
	s.handles[sessionID] = handle
	s.handlesMu.Unlock()

	// Background goroutine: clean up handle when process exits.
	go func() {
		<-handle.Done
		s.handlesMu.Lock()
		delete(s.handles, sessionID)
		s.handlesMu.Unlock()
	}()

	row, err := s.Queries().GetSession(ctx, sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get session after launch: %w", err))
	}

	return connect.NewResponse(&gruv1.LaunchSessionResponse{
		Session: sessionRowToProto(row),
	}), nil
}

// --- KillSession ---

func (s *Service) KillSession(
	ctx context.Context,
	req *connect.Request[gruv1.KillSessionRequest],
) (*connect.Response[gruv1.KillSessionResponse], error) {
	sessionID := req.Msg.Id

	row, err := s.Queries().GetSession(ctx, sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %q not found: %w", sessionID, err))
	}
	_ = row // used for future status checks

	s.handlesMu.Lock()
	handle, ok := s.handles[sessionID]
	s.handlesMu.Unlock()

	if ok && handle.Kill != nil {
		killCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := handle.Kill(killCtx); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("kill: %w", err))
		}
	}

	_, err = s.Queries().UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		Status: "killed",
		ID:     sessionID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("update status: %w", err))
	}

	return connect.NewResponse(&gruv1.KillSessionResponse{Success: true}), nil
}

// --- helpers ---

func (s *Service) upsertProject(ctx context.Context, projectDir string) (string, error) {
	name := filepath.Base(projectDir)
	row, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID:      uuid.NewString(),
		Name:    name,
		Path:    projectDir,
		Runtime: "claude-code",
	})
	if err != nil {
		return "", err
	}
	return row.ID, nil
}

func nilString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func sessionRowToProto(row store.Session) *gruv1.Session {
	s := &gruv1.Session{
		Id:          row.ID,
		ProjectId:   row.ProjectID,
		Runtime:     row.Runtime,
		Profile:     derefString(row.Profile),
		TmuxSession: derefString(row.TmuxSession),
		TmuxWindow:  derefString(row.TmuxWindow),
	}
	s.Status = statusStringToProto(row.Status)
	if row.Pid != nil {
		s.Pid = *row.Pid
	}
	return s
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func statusStringToProto(status string) gruv1.SessionStatus {
	switch status {
	case "starting":
		return gruv1.SessionStatus_SESSION_STATUS_STARTING
	case "running":
		return gruv1.SessionStatus_SESSION_STATUS_RUNNING
	case "idle":
		return gruv1.SessionStatus_SESSION_STATUS_IDLE
	case "needs_attention":
		return gruv1.SessionStatus_SESSION_STATUS_NEEDS_ATTENTION
	case "completed":
		return gruv1.SessionStatus_SESSION_STATUS_COMPLETED
	case "errored":
		return gruv1.SessionStatus_SESSION_STATUS_ERRORED
	case "killed":
		return gruv1.SessionStatus_SESSION_STATUS_KILLED
	default:
		return gruv1.SessionStatus_SESSION_STATUS_UNSPECIFIED
	}
}
```

- [ ] **Step 5: Run the tests — confirm they pass**

```bash
go test ./internal/server/...
```

Expected:
```
ok  	github.com/dakshjotwani/gru/internal/server	0.XXXs
```

- [ ] **Step 6: Commit**

```bash
git add internal/server/service.go internal/server/service_test.go
git commit -m "feat: implement LaunchSession and KillSession gRPC handlers"
```

---

### Task 5: Wire Supervisor into Server Startup

**Files:**
- Modify: `cmd/gru/server.go`

This wires the supervisor into the server startup so it runs in the background alongside the gRPC server.

- [ ] **Step 1: Read the current server.go**

```bash
cat cmd/gru/server.go
```

- [ ] **Step 2: Add supervisor wiring to `cmd/gru/server.go`**

Locate the `runServer` function (or equivalent) and add supervisor startup after the store is initialized. Add a `supervisorStore` adapter and start `supervisor.Run` in a goroutine:

```go
// Add at the top with other imports:
// "github.com/dakshjotwani/gru/internal/supervisor"

// After store initialization in runServer, add:

sv := supervisor.New(
    &supervisorStoreAdapter{queries: st.Queries()},
    &supervisorPublisherAdapter{pub: broadcaster},
    10*time.Second,
)

// Start startup reconciliation and polling loop.
go sv.Run(serverCtx)
```

The two adapters bridge the supervisor interfaces to the concrete store/publisher types. Add them at the bottom of `server.go`:

```go
// supervisorStoreAdapter adapts store.Querier to supervisor.SessionStore.
type supervisorStoreAdapter struct {
	queries store.Querier
}

func (a *supervisorStoreAdapter) ListLiveSessions(ctx context.Context) ([]supervisor.LiveSession, error) {
	rows, err := a.queries.ListSessions(ctx, store.ListSessionsParams{
		ProjectID: "",
		Status:    "", // list all
	})
	if err != nil {
		return nil, err
	}
	var live []supervisor.LiveSession
	for _, r := range rows {
		if r.Status != "running" && r.Status != "starting" {
			continue
		}
		live = append(live, supervisor.LiveSession{
			ID:          r.ID,
			TmuxSession: derefString(r.TmuxSession),
			TmuxWindow:  derefString(r.TmuxWindow),
			Status:      r.Status,
		})
	}
	return live, nil
}

func (a *supervisorStoreAdapter) MarkSessionErrored(ctx context.Context, sessionID string) error {
	_, err := a.queries.UpdateSessionStatus(ctx, store.UpdateSessionStatusParams{
		Status: "errored",
		ID:     sessionID,
	})
	return err
}

// supervisorPublisherAdapter adapts the broadcaster to supervisor.EventPublisher.
type supervisorPublisherAdapter struct {
	pub Publisher // the same broadcaster used by the gRPC service
}

func (a *supervisorPublisherAdapter) PublishCrash(ctx context.Context, e supervisor.CrashEvent) {
	a.pub.Publish(store.GruEvent{
		Type:      "session.crash",
		SessionID: e.SessionID,
	})
}
```

- [ ] **Step 3: Verify the server package compiles**

```bash
go build ./cmd/gru/...
```

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add cmd/gru/server.go
git commit -m "feat: wire process liveness supervisor into server startup"
```

---

### Task 6: CLI Commands (Cobra)

**Files:**
- Create: `cmd/gru/root.go` — root cobra command with persistent `--server` and `--api-key` flags
- Create: `cmd/gru/cmd_status.go`
- Create: `cmd/gru/cmd_kill.go`
- Create: `cmd/gru/cmd_launch.go`
- Create: `cmd/gru/cmd_tail.go`
- Create: `cmd/gru/cmd_attach.go` — `gru attach <id-or-project>`
- Create: `cmd/gru/root_test.go` — tests using cobra's `SetArgs` + `SetOut`
- Modify: `cmd/gru/main.go` — delegates to cobra's `Execute()`

- [ ] **Step 1: Add Cobra dependency**

```bash
go get github.com/spf13/cobra@latest
```

Expected: `go.mod` updated with `github.com/spf13/cobra`.

- [ ] **Step 2: Write the failing CLI tests first**

Create `cmd/gru/root_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeSrv implements gruv1connect.GruServiceHandler for CLI tests.
type fakeSrv struct {
	gruv1connect.UnimplementedGruServiceHandler
	sessions []*gruv1.Session
}

func (f *fakeSrv) ListSessions(_ context.Context, _ *connect.Request[gruv1.ListSessionsRequest]) (*connect.Response[gruv1.ListSessionsResponse], error) {
	return connect.NewResponse(&gruv1.ListSessionsResponse{Sessions: f.sessions}), nil
}

func (f *fakeSrv) GetSession(_ context.Context, req *connect.Request[gruv1.GetSessionRequest]) (*connect.Response[gruv1.Session], error) {
	for _, s := range f.sessions {
		if s.Id == req.Msg.Id || strings.HasPrefix(s.Id, req.Msg.Id) {
			return connect.NewResponse(s), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, nil)
}

func (f *fakeSrv) KillSession(_ context.Context, req *connect.Request[gruv1.KillSessionRequest]) (*connect.Response[gruv1.KillSessionResponse], error) {
	for _, s := range f.sessions {
		if s.Id == req.Msg.Id || strings.HasPrefix(s.Id, req.Msg.Id) {
			return connect.NewResponse(&gruv1.KillSessionResponse{Success: true}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, nil)
}

func (f *fakeSrv) LaunchSession(_ context.Context, _ *connect.Request[gruv1.LaunchSessionRequest]) (*connect.Response[gruv1.LaunchSessionResponse], error) {
	sess := &gruv1.Session{
		Id:        "new-session-abc",
		ProjectId: "proj-1",
		Runtime:   "claude-code",
		Status:    gruv1.SessionStatus_SESSION_STATUS_STARTING,
		StartedAt: timestamppb.Now(),
	}
	return connect.NewResponse(&gruv1.LaunchSessionResponse{Session: sess}), nil
}

// startFakeServer starts an httptest server running the fake gRPC service.
func startFakeServer(t *testing.T, srv *fakeSrv) (url string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(gruv1connect.NewGruServiceHandler(srv))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL
}

// runCLI executes the root cobra command with the given args against the fake server.
func runCLI(t *testing.T, serverURL string, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	// Prepend persistent flags so tests don't need config on disk.
	fullArgs := append([]string{"--server", serverURL, "--api-key", "test-key"}, args...)
	root.SetArgs(fullArgs)
	if err := root.Execute(); err != nil {
		t.Fatalf("CLI error: %v", err)
	}
	return buf.String()
}

func TestCLI_Status_List(t *testing.T) {
	now := timestamppb.New(time.Now().Add(-5 * time.Minute))
	srv := &fakeSrv{sessions: []*gruv1.Session{
		{
			Id:             "abcd1234-efgh-ijkl-mnop-qrstuvwxyz00",
			ProjectId:      "proj-alpha",
			Runtime:        "claude-code",
			Status:         gruv1.SessionStatus_SESSION_STATUS_RUNNING,
			AttentionScore: 0.8,
			StartedAt:      now,
		},
	}}
	out := runCLI(t, startFakeServer(t, srv), "status")

	if !strings.Contains(out, "abcd1234") {
		t.Errorf("output missing session ID prefix: %q", out)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("output missing status: %q", out)
	}
	if !strings.Contains(out, "proj-alpha") {
		t.Errorf("output missing project ID: %q", out)
	}
}

func TestCLI_Status_Single(t *testing.T) {
	now := timestamppb.Now()
	srv := &fakeSrv{sessions: []*gruv1.Session{
		{
			Id:             "abcd1234-0000-0000-0000-000000000001",
			ProjectId:      "proj-beta",
			Runtime:        "claude-code",
			Status:         gruv1.SessionStatus_SESSION_STATUS_IDLE,
			AttentionScore: 0.3,
			StartedAt:      now,
			Pid:            42,
		},
	}}
	out := runCLI(t, startFakeServer(t, srv), "status", "abcd1234")

	if !strings.Contains(out, "abcd1234") {
		t.Errorf("output missing session ID: %q", out)
	}
	if !strings.Contains(out, "idle") {
		t.Errorf("output missing status: %q", out)
	}
	if !strings.Contains(out, "42") {
		t.Errorf("output missing PID: %q", out)
	}
}

func TestCLI_Kill(t *testing.T) {
	srv := &fakeSrv{sessions: []*gruv1.Session{
		{Id: "kill-me-00000-00000", Status: gruv1.SessionStatus_SESSION_STATUS_RUNNING},
	}}
	out := runCLI(t, startFakeServer(t, srv), "kill", "kill-me")

	if !strings.Contains(out, "killed") && !strings.Contains(out, "success") {
		t.Errorf("output should indicate success: %q", out)
	}
}

func TestCLI_Launch(t *testing.T) {
	srv := &fakeSrv{}
	out := runCLI(t, startFakeServer(t, srv), "launch", "/tmp", "do something")

	if !strings.Contains(out, "new-session-abc") {
		t.Errorf("output missing new session ID: %q", out)
	}
}

func TestCLI_Attach_ShowsTmuxCommand(t *testing.T) {
	srv := &fakeSrv{sessions: []*gruv1.Session{
		{
			Id:          "abcd1234-0000-0000-0000-000000000001",
			TmuxSession: "gru-av-sim",
			TmuxWindow:  "feat-dev·abcd1234",
		},
	}}
	// attach uses syscall.Exec which can't be tested directly
	// Test that it returns an error for a session with no tmux info
	out := runCLIErr(t, startFakeServer(t, srv), "attach", "no-tmux-session")
	// verify error message mentions "no tmux session"
	if !strings.Contains(out, "no tmux session") {
		t.Errorf("expected 'no tmux session' in output: %q", out)
	}
}

// runCLIErr is like runCLI but expects the command to return an error.
func runCLIErr(t *testing.T, serverURL string, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	fullArgs := append([]string{"--server", serverURL, "--api-key", "test-key"}, args...)
	root.SetArgs(fullArgs)
	_ = root.Execute() // ignore error — caller checks output
	return buf.String()
}
```

- [ ] **Step 3: Run tests — confirm compile failure**

```bash
go test ./cmd/gru/... -run TestCLI
```

Expected: compile error — `newRootCmd` not defined yet.

- [ ] **Step 4: Create `cmd/gru/root.go`**

The root command holds persistent flags (`--server`, `--api-key`) and builds the gRPC client once for all subcommands.

```go
package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/dakshjotwani/gru/internal/config"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type rootState struct {
	serverURL string
	apiKey    string
	client    gruv1connect.GruServiceClient
}

func newRootCmd() *cobra.Command {
	state := &rootState{}

	root := &cobra.Command{
		Use:   "gru",
		Short: "Mission control for AI agent fleets",
		Long:  "Gru monitors, launches, and manages AI coding agent sessions across projects.",
		// SilenceUsage prevents cobra from printing usage on every error.
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip config loading for the server subcommand (it loads its own config).
			if cmd.Name() == "server" {
				return nil
			}
			if state.serverURL == "" {
				cfg, err := config.Load(defaultConfigPath())
				if err != nil {
					return fmt.Errorf("load config: %w", err)
				}
				state.serverURL = "http://" + cfg.Addr
				if state.apiKey == "" {
					state.apiKey = cfg.APIKey
				}
			}
			state.client = gruv1connect.NewGruServiceClient(
				&http.Client{Timeout: 30 * time.Second},
				state.serverURL,
			)
			return nil
		},
	}

	root.PersistentFlags().StringVar(&state.serverURL, "server", "", "gru server URL (default: from ~/.gru/server.yaml)")
	root.PersistentFlags().StringVar(&state.apiKey, "api-key", "", "API key (default: from ~/.gru/server.yaml)")

	root.AddCommand(
		newServerCmd(),
		newStatusCmd(state),
		newKillCmd(state),
		newLaunchCmd(state),
		newTailCmd(state),
		newAttachCmd(state),
	)

	return root
}

// authReq adds the Bearer token header to any connect request.
func (s *rootState) authReq(req interface{ Header() http.Header }) {
	req.Header().Set("Authorization", "Bearer "+s.apiKey)
}

// ── formatting helpers (shared across subcommands) ────────────────────────

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func statusLabel(s gruv1.SessionStatus) string {
	switch s {
	case gruv1.SessionStatus_SESSION_STATUS_STARTING:
		return "starting"
	case gruv1.SessionStatus_SESSION_STATUS_RUNNING:
		return "running"
	case gruv1.SessionStatus_SESSION_STATUS_IDLE:
		return "idle"
	case gruv1.SessionStatus_SESSION_STATUS_NEEDS_ATTENTION:
		return "needs_attention"
	case gruv1.SessionStatus_SESSION_STATUS_COMPLETED:
		return "completed"
	case gruv1.SessionStatus_SESSION_STATUS_ERRORED:
		return "errored"
	case gruv1.SessionStatus_SESSION_STATUS_KILLED:
		return "killed"
	default:
		return "unknown"
	}
}

func formatUptime(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return "-"
	}
	d := time.Since(ts.AsTime()).Round(time.Second)
	if d < 0 {
		return "0s"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, sec)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

func hrule(w int) string { return strings.Repeat("-", w) }
```

- [ ] **Step 5: Create `cmd/gru/cmd_status.go`**

```go
package main

import (
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newStatusCmd(s *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "status [id]",
		Short: "List all sessions, or show detail for one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			if len(args) == 1 {
				req := connect.NewRequest(&gruv1.GetSessionRequest{Id: args[0]})
				s.authReq(req)
				resp, err := s.client.GetSession(ctx, req)
				if err != nil {
					return fmt.Errorf("get session: %w", err)
				}
				sess := resp.Msg
				fmt.Fprintf(out, "ID:       %s\n", sess.Id)
				fmt.Fprintf(out, "Project:  %s\n", sess.ProjectId)
				fmt.Fprintf(out, "Runtime:  %s\n", sess.Runtime)
				fmt.Fprintf(out, "Status:   %s\n", statusLabel(sess.Status))
				fmt.Fprintf(out, "Attn:     %.2f\n", sess.AttentionScore)
				fmt.Fprintf(out, "PID:      %d\n", sess.Pid)
				if sess.StartedAt != nil {
					fmt.Fprintf(out, "Started:  %s\n", sess.StartedAt.AsTime().Format("2006-01-02 15:04:05"))
					fmt.Fprintf(out, "Uptime:   %s\n", formatUptime(sess.StartedAt))
				}
				if sess.Profile != "" {
					fmt.Fprintf(out, "Profile:  %s\n", sess.Profile)
				}
				return nil
			}

			req := connect.NewRequest(&gruv1.ListSessionsRequest{})
			s.authReq(req)
			resp, err := s.client.ListSessions(ctx, req)
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			sessions := resp.Msg.Sessions
			if len(sessions) == 0 {
				fmt.Fprintln(out, "no sessions")
				return nil
			}
			fmt.Fprintf(out, "%-12s  %-20s  %-18s  %-6s  %s\n", "ID", "PROJECT", "STATUS", "ATTN", "UPTIME")
			fmt.Fprintln(out, hrule(72))
			for _, sess := range sessions {
				fmt.Fprintf(out, "%-12s  %-20s  %-18s  %-6.2f  %s\n",
					shortID(sess.Id), shortID(sess.ProjectId),
					statusLabel(sess.Status), sess.AttentionScore,
					formatUptime(sess.StartedAt))
			}
			return nil
		},
	}
}
```

- [ ] **Step 6: Create `cmd/gru/cmd_kill.go`**

```go
package main

import (
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newKillCmd(s *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "kill <id>",
		Short: "Terminate a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := connect.NewRequest(&gruv1.KillSessionRequest{Id: args[0]})
			s.authReq(req)
			resp, err := s.client.KillSession(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("kill session: %w", err)
			}
			if resp.Msg.Success {
				fmt.Fprintf(cmd.OutOrStdout(), "session %s killed successfully\n", shortID(args[0]))
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "kill did not succeed for session %s\n", shortID(args[0]))
			}
			return nil
		},
	}
}
```

- [ ] **Step 7: Create `cmd/gru/cmd_launch.go`**

```go
package main

import (
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newLaunchCmd(s *rootState) *cobra.Command {
	var profile string

	cmd := &cobra.Command{
		Use:   "launch <dir> <prompt>",
		Short: "Start a new agent session",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := connect.NewRequest(&gruv1.LaunchSessionRequest{
				ProjectDir: args[0],
				Prompt:     args[1],
				Profile:    profile,
			})
			s.authReq(req)
			resp, err := s.client.LaunchSession(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("launch session: %w", err)
			}
			sess := resp.Msg.Session
			fmt.Fprintf(cmd.OutOrStdout(), "launched session %s (status: %s)\n",
				sess.Id, statusLabel(sess.Status))
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "", "agent profile name (from .gru/config.yaml)")
	return cmd
}
```

- [ ] **Step 8: Create `cmd/gru/cmd_tail.go`**

```go
package main

import (
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newTailCmd(s *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "tail <session-id>",
		Short: "Stream live events for a session (Ctrl+C to stop)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			req := connect.NewRequest(&gruv1.SubscribeEventsRequest{
				ProjectIds: []string{args[0]},
			})
			s.authReq(req)

			stream, err := s.client.SubscribeEvents(ctx, req)
			if err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
			defer stream.Close()

			fmt.Fprintf(out, "tailing events for %s (Ctrl+C to stop)...\n", shortID(args[0]))
			for stream.Receive() {
				ev := stream.Msg()
				ts := "-"
				if ev.Timestamp != nil {
					ts = ev.Timestamp.AsTime().Format("15:04:05")
				}
				fmt.Fprintf(out, "[%s] %-30s  session=%s\n", ts, ev.Type, shortID(ev.SessionId))
			}
			if err := stream.Err(); err != nil && ctx.Err() == nil {
				return fmt.Errorf("stream error: %w", err)
			}
			return nil
		},
	}
}
```

- [ ] **Step 9: Create `cmd/gru/cmd_attach.go`**

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newAttachCmd(s *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <session-id-or-project-name>",
		Short: "Attach to a running session in tmux",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Look up the tmux session name for this session ID.
			ctx := cmd.Context()
			req := connect.NewRequest(&gruv1.GetSessionRequest{Id: args[0]})
			s.authReq(req)
			resp, err := s.client.GetSession(ctx, req)
			if err != nil {
				// Try treating the arg as a project name → attach to gru-<arg>
				tmuxSession := "gru-" + sanitizeProjectName(args[0])
				return execTmuxAttach(tmuxSession, "")
			}
			sess := resp.Msg
			if sess.TmuxSession == "" {
				return fmt.Errorf("session %s has no tmux session (not launched by gru)", shortID(args[0]))
			}
			return execTmuxAttach(sess.TmuxSession, sess.TmuxWindow)
		},
	}
}

// sanitizeProjectName lowercases and replaces /, spaces, dots with -.
// Used to derive the tmux session name from a project name argument.
func sanitizeProjectName(name string) string {
	result := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			result = append(result, c+32) // lowercase
		case c == '/' || c == ' ' || c == '.':
			result = append(result, '-')
		default:
			result = append(result, c)
		}
	}
	s := string(result)
	if len(s) > 4 && s[:4] == "gru-" {
		s = s[4:]
	}
	return s
}

// execTmuxAttach replaces the current process with tmux attach.
func execTmuxAttach(session, window string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found in PATH: %w", err)
	}
	args := []string{"tmux", "attach-session", "-t", session}
	if window != "" {
		// After attaching, select the specific window.
		args = append(args, ";", "select-window", "-t", window)
	}
	return syscall.Exec(tmuxPath, args, os.Environ())
}
```

- [ ] **Step 10: Update `cmd/gru/main.go`**

```go
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

Note: `newServerCmd()` is defined in `cmd/gru/server.go` (from Plan 1a Task 11) — update it to return a `*cobra.Command` instead of being called from `run()`:

```go
// In cmd/gru/server.go — replace runServer() with:
func newServerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "server",
		Short: "Start the gru server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer()
		},
	}
}
```

- [ ] **Step 11: Run the tests — confirm they pass**

```bash
go test ./cmd/gru/...
```

Expected:
```
ok  	github.com/dakshjotwani/gru/cmd/gru	0.XXXs
```

- [ ] **Step 12: Verify binary and help output**

```bash
go build ./cmd/gru/... && ./gru --help
```

Expected:
```
Gru monitors, launches, and manages AI coding agent sessions across projects.

Usage:
  gru [command]

Available Commands:
  attach      Attach to a running session in tmux
  kill        Terminate a session
  launch      Start a new agent session
  server      Start the gru server
  status      List all sessions, or show detail for one
  tail        Stream live events for a session (Ctrl+C to stop)

Flags:
      --api-key string   API key (default: from ~/.gru/server.yaml)
  -h, --help             help for gru
      --server string    gru server URL (default: from ~/.gru/server.yaml)

Use "gru [command] --help" for more information about a command.
```

- [ ] **Step 12: Commit**

```bash
git add cmd/gru/ go.mod go.sum
git commit -m "feat: add Cobra CLI with status, kill, launch, tail, attach commands"
```

---

### Task 7: Full Integration Smoke Test

- [ ] **Step 1: Run all tests**

```bash
go test ./...
```

Expected: all packages pass with no failures.

```
ok  	github.com/dakshjotwani/gru/internal/controller	0.XXXs
ok  	github.com/dakshjotwani/gru/internal/controller/claude	0.XXXs
ok  	github.com/dakshjotwani/gru/internal/supervisor	0.XXXs
ok  	github.com/dakshjotwani/gru/internal/server	0.XXXs
ok  	github.com/dakshjotwani/gru/cmd/gru	0.XXXs
```

- [ ] **Step 2: Vet the codebase**

```bash
go vet ./...
```

Expected: no warnings.

- [ ] **Step 3: Build the final binary**

```bash
go build -o /tmp/gru-1c ./cmd/gru/...
/tmp/gru-1c help
```

Expected output:
```
Usage: gru <command> [args]
Commands:
  status [id]        list all sessions, or show one
  kill <id>          terminate a session
  launch <dir> <p>   start a new session
  tail <id>          stream events for a session
```

- [ ] **Step 4: Final commit**

```bash
git add -A
git commit -m "chore: phase 1c complete — session control, supervisor, CLI"
```

---

## Self-Review Checklist

1. **Complete code (no placeholders)?**
   - All function bodies are written in full. No `// TODO` or `panic("not implemented")` stubs remain.

2. **Test written first for every task?**
   - Task 1: `controller_test.go` written before `controller.go`
   - Task 2: `controller_test.go` written before `claude/controller.go`
   - Task 3: `supervisor_test.go` written before `supervisor.go`
   - Task 4: new tests in `service_test.go` written before `service.go` changes
   - Task 6: `cli_test.go` written before `cli.go`

3. **Type/function name consistency?**
   - `controller.SessionController`, `controller.Registry`, `controller.LaunchOptions`, `controller.SessionHandle` — used consistently across `controller.go`, `claude/controller.go`, `service.go`, and tests.
   - `SessionHandle.TmuxSession` and `SessionHandle.TmuxWindow` replace `PID`/`PGID` — propagated through service, DB, proto, and supervisor.
   - `supervisor.SessionStore`, `supervisor.EventPublisher`, `supervisor.LiveSession`, `supervisor.CrashEvent` — used consistently across `supervisor.go`, `supervisor_test.go`, and `server.go` adapters.
   - `newRootCmd` is the single entry point for CLI dispatch, used in both `root_test.go` and `main.go`.

4. **Full scope coverage?**
   - SessionController interface + registry: Task 1
   - Claude Code controller with tmux-based launch + kill: Task 2
   - LaunchSession + KillSession gRPC with TmuxSession/TmuxWindow: Task 4
   - Process liveness supervisor using tmux window polling (ReconcileOnce + Run): Task 3
   - Server startup wiring: Task 5
   - CLI: status, status \<id\>, kill, launch, tail, attach: Task 6
