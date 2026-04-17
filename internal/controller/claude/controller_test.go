package claude_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/controller"
	claudectrl "github.com/dakshjotwani/gru/internal/controller/claude"
	"github.com/dakshjotwani/gru/internal/env"
)

// fakeEnv is an in-memory env.Environment that records every Exec/ExecPty call
// and succeeds unconditionally. It is just enough for the controller tests —
// real conformance happens in internal/env/host.
type fakeEnv struct {
	mu        sync.Mutex
	execCalls [][]string
	created   map[string]env.Instance
	destroyed map[string]bool
}

func newFakeEnv() *fakeEnv {
	return &fakeEnv{
		created:   make(map[string]env.Instance),
		destroyed: make(map[string]bool),
	}
}

func (f *fakeEnv) RuntimeID() string { return "host" }

func (f *fakeEnv) Create(_ context.Context, spec env.EnvSpec) (env.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst := env.Instance{
		ID:         spec.Name,
		Adapter:    "host",
		PtyHolders: []string{"tmux"},
		StartedAt:  time.Now(),
	}
	f.created[spec.Name] = inst
	return inst, nil
}

func (f *fakeEnv) Rehydrate(_ context.Context, providerRef string) (env.Instance, error) {
	return env.Instance{Adapter: "host", ProviderRef: providerRef, PtyHolders: []string{"tmux"}}, nil
}

func (f *fakeEnv) Exec(_ context.Context, _ env.Instance, cmd []string) (env.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCalls = append(f.execCalls, cmd)
	// tmux has-session returns non-zero when the session doesn't exist. By
	// returning ExitCode=1 we tell PersistentPty "no session yet, go ahead
	// and create one", which is what every Start call expects.
	if len(cmd) >= 2 && cmd[0] == "tmux" && cmd[1] == "has-session" {
		return env.ExecResult{ExitCode: 1}, nil
	}
	return env.ExecResult{ExitCode: 0}, nil
}

func (f *fakeEnv) ExecPty(_ context.Context, _ env.Instance, _ []string) (io.ReadWriteCloser, error) {
	return nil, nil
}

func (f *fakeEnv) Destroy(_ context.Context, inst env.Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed[inst.ID] = true
	return nil
}

func (f *fakeEnv) Events(_ context.Context, _ env.Instance) (<-chan env.Event, error) {
	ch := make(chan env.Event)
	close(ch)
	return ch, nil
}

func (f *fakeEnv) Status(_ context.Context, _ env.Instance) (env.Status, error) {
	return env.Status{Running: true}, nil
}

func (f *fakeEnv) calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.execCalls))
	copy(out, f.execCalls)
	return out
}

func TestClaudeController_RuntimeID(t *testing.T) {
	c := claudectrl.NewClaudeController("key", "localhost", "7070", newFakeEnv())
	if got := c.RuntimeID(); got != "claude-code" {
		t.Errorf("RuntimeID = %q, want %q", got, "claude-code")
	}
}

func TestClaudeController_Capabilities(t *testing.T) {
	c := claudectrl.NewClaudeController("key", "localhost", "7070", newFakeEnv())
	caps := c.Capabilities()
	if len(caps) != 1 || caps[0] != controller.CapKill {
		t.Errorf("Capabilities = %v, want [kill]", caps)
	}
}

func TestClaudeController_Launch_StartsTmuxSessionAndWritesLookup(t *testing.T) {
	fe := newFakeEnv()
	c := claudectrl.NewClaudeController("key", "localhost", "7070", fe)
	projectDir := t.TempDir()

	handle, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID:  "abcd1234-0000-0000-0000-000000000001",
		ProjectDir: projectDir,
		Prompt:     "hello world",
		Profile:    "feat-dev",
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if handle.TmuxSession != "gru-abcd1234" {
		t.Errorf("TmuxSession = %q, want %q", handle.TmuxSession, "gru-abcd1234")
	}
	if handle.TmuxWindow != "" {
		t.Errorf("TmuxWindow = %q, want empty (v2 one-session-per-window layout)", handle.TmuxWindow)
	}

	var sawNewSession bool
	for _, call := range fe.calls() {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "new-session") && strings.Contains(joined, "GRU_SESSION_ID") {
			sawNewSession = true
		}
	}
	if !sawNewSession {
		t.Error("PersistentPty did not issue tmux new-session with env vars")
	}
}

func TestClaudeController_Launch_AddDirsForwardedToClaudeCLI(t *testing.T) {
	fe := newFakeEnv()
	c := claudectrl.NewClaudeController("key", "localhost", "7070", fe)
	primary := t.TempDir()
	secondary := t.TempDir()
	tertiary := t.TempDir()

	_, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID:  "abcd1234-0000-0000-0000-000000000001",
		ProjectDir: primary,
		Prompt:     "test",
		AddDirs:    []string{secondary, "", tertiary}, // empty string skipped
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	var launched string
	for _, call := range fe.calls() {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "new-session") && strings.Contains(joined, "GRU_SESSION_ID") {
			launched = joined
			break
		}
	}
	if launched == "" {
		t.Fatal("new-session call with env vars not found")
	}
	if !strings.Contains(launched, "--add-dir "+secondary) {
		t.Errorf("expected --add-dir for secondary workdir in %q", launched)
	}
	if !strings.Contains(launched, "--add-dir "+tertiary) {
		t.Errorf("expected --add-dir for tertiary workdir in %q", launched)
	}
	if strings.Contains(launched, "--add-dir \"\"") || strings.Contains(launched, "--add-dir ''") {
		t.Errorf("empty AddDirs entry leaked into argv: %s", launched)
	}
}

func TestClaudeController_Kill_DestroysEnvInstance(t *testing.T) {
	fe := newFakeEnv()
	c := claudectrl.NewClaudeController("key", "localhost", "7070", fe)
	projectDir := t.TempDir()
	sessionID := "abcd1234-0000-0000-0000-000000000099"

	if _, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID:  sessionID,
		ProjectDir: projectDir,
	}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err := c.Kill(context.Background(), sessionID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	fe.mu.Lock()
	destroyed := fe.destroyed[sessionID]
	fe.mu.Unlock()
	if !destroyed {
		t.Errorf("Kill did not call env.Destroy for session %s", sessionID)
	}
}
