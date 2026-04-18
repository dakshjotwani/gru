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

func (f *fakeEnv) AgentArgs(_ context.Context, _ env.Instance) (env.AgentArgs, error) {
	return env.AgentArgs{}, nil
}

func (f *fakeEnv) calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.execCalls))
	copy(out, f.execCalls)
	return out
}

// registryWith returns an env.Registry containing one adapter. Every existing
// controller test used a fakeEnv with RuntimeID="host", which is also the
// default adapter name the controller looks up when LaunchOptions.EnvSpec is
// nil — so these tests stay functionally unchanged.
func registryWith(e env.Environment) *env.Registry {
	r := env.NewRegistry()
	r.Register(e)
	return r
}

func TestClaudeController_RuntimeID(t *testing.T) {
	c := claudectrl.NewClaudeController("key", "localhost", "7070", registryWith(newFakeEnv()), "host")
	if got := c.RuntimeID(); got != "claude-code" {
		t.Errorf("RuntimeID = %q, want %q", got, "claude-code")
	}
}

func TestClaudeController_Capabilities(t *testing.T) {
	c := claudectrl.NewClaudeController("key", "localhost", "7070", registryWith(newFakeEnv()), "host")
	caps := c.Capabilities()
	if len(caps) != 1 || caps[0] != controller.CapKill {
		t.Errorf("Capabilities = %v, want [kill]", caps)
	}
}

func TestClaudeController_Launch_StartsTmuxSessionAndWritesLookup(t *testing.T) {
	fe := newFakeEnv()
	c := claudectrl.NewClaudeController("key", "localhost", "7070", registryWith(fe), "host")
	projectDir := t.TempDir()

	handle, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID: "abcd1234-0000-0000-0000-000000000001",
		Prompt:    "hello world",
		Profile:   "feat-dev",
		EnvSpec: env.EnvSpec{
			Adapter:  "host",
			Workdirs: []string{projectDir},
		},
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

// TestClaudeController_Launch_WorkdirsAppendedAsAddDir verifies that
// spec.Workdirs[1..] turn into --add-dir flags on the claude invocation.
// (Workdirs[0] is the primary cwd and does NOT get a flag.)
func TestClaudeController_Launch_WorkdirsAppendedAsAddDir(t *testing.T) {
	fe := newFakeEnv()
	c := claudectrl.NewClaudeController("key", "localhost", "7070", registryWith(fe), "host")
	primary := t.TempDir()
	secondary := t.TempDir()
	tertiary := t.TempDir()

	_, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID: "abcd1234-0000-0000-0000-000000000001",
		Prompt:    "test",
		EnvSpec: env.EnvSpec{
			Adapter:  "host",
			Workdirs: []string{primary, secondary, tertiary},
		},
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
	// primary must NOT appear as an --add-dir — it's the cwd.
	if strings.Contains(launched, "--add-dir "+primary) {
		t.Errorf("primary workdir leaked into --add-dir: %s", launched)
	}
}

// TestClaudeController_Launch_PicksAdapterFromEnvSpec verifies that when
// LaunchOptions.EnvSpec is set, the controller resolves the adapter by
// EnvSpec.Adapter instead of falling back to the default. Regression guard
// for the refactor that turned a single envAdp into an env.Registry.
func TestClaudeController_Launch_PicksAdapterFromEnvSpec(t *testing.T) {
	hostFake := newFakeEnv() // RuntimeID "host"
	cmdFake := &fakeEnvNamed{fakeEnv: newFakeEnv(), id: "command"}
	reg := env.NewRegistry()
	reg.Register(hostFake)
	reg.Register(cmdFake)

	c := claudectrl.NewClaudeController("key", "localhost", "7070", reg, "host")
	projectDir := t.TempDir()

	_, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID: "abcd1234-0000-0000-0000-000000000777",
		EnvSpec: env.EnvSpec{
			Adapter:  "command",
			Workdirs: []string{projectDir},
			Config:   map[string]any{"mode": "fullstack"},
		},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Command adapter should have been used, not host.
	if len(cmdFake.created) != 1 {
		t.Errorf("command adapter Create calls = %d, want 1", len(cmdFake.created))
	}
	hostFake.mu.Lock()
	if len(hostFake.created) != 0 {
		t.Errorf("host adapter Create calls = %d, want 0 (should not have been used)", len(hostFake.created))
	}
	hostFake.mu.Unlock()

	// Spec.Name is overridden by the controller — spec file identity
	// should not leak into the instance ID.
	if _, ok := cmdFake.created["abcd1234-0000-0000-0000-000000000777"]; !ok {
		t.Errorf("expected session id as instance name, got creates: %v", cmdFake.created)
	}
}

// TestClaudeController_Launch_UnknownAdapter verifies that asking for an
// adapter not in the registry fails cleanly rather than panicking.
func TestClaudeController_Launch_UnknownAdapter(t *testing.T) {
	reg := env.NewRegistry()
	reg.Register(newFakeEnv())
	c := claudectrl.NewClaudeController("key", "localhost", "7070", reg, "host")

	_, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID: "abcd1234-0000-0000-0000-000000000888",
		EnvSpec: env.EnvSpec{
			Adapter:  "kubernetes",
			Workdirs: []string{t.TempDir()},
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown adapter, got nil")
	}
	if !strings.Contains(err.Error(), "kubernetes") {
		t.Errorf("error %q should mention the adapter name", err)
	}
}

// TestClaudeController_NewPanicsOnUnknownDefault guards the constructor's
// fail-fast contract: a default adapter that isn't in the registry is a
// deployment bug and should surface immediately, not on first Launch.
func TestClaudeController_NewPanicsOnUnknownDefault(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected panic when defaultAdapter is not in registry")
		}
	}()
	reg := env.NewRegistry() // empty
	_ = claudectrl.NewClaudeController("key", "localhost", "7070", reg, "host")
}

// fakeEnvNamed lets a fakeEnv be registered under a runtime ID other than
// "host" — useful for the multi-adapter tests above.
type fakeEnvNamed struct {
	*fakeEnv
	id string
}

func (f *fakeEnvNamed) RuntimeID() string { return f.id }

func (f *fakeEnvNamed) Create(ctx context.Context, spec env.EnvSpec) (env.Instance, error) {
	inst, err := f.fakeEnv.Create(ctx, spec)
	if err == nil {
		inst.Adapter = f.id
	}
	return inst, err
}

func TestClaudeController_Kill_DestroysEnvInstance(t *testing.T) {
	fe := newFakeEnv()
	c := claudectrl.NewClaudeController("key", "localhost", "7070", registryWith(fe), "host")
	projectDir := t.TempDir()
	sessionID := "abcd1234-0000-0000-0000-000000000099"

	if _, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID: sessionID,
		EnvSpec: env.EnvSpec{
			Adapter:  "host",
			Workdirs: []string{projectDir},
		},
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
