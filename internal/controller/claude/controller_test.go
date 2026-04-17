package claude_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dakshjotwani/gru/internal/controller"
	claudectrl "github.com/dakshjotwani/gru/internal/controller/claude"
)

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
	if handle.SessionID == "" {
		t.Error("SessionID is empty")
	}

	var foundSession bool
	for _, call := range ft.runs {
		if len(call) > 0 && call[0] == "new-session" {
			foundSession = true
		}
	}
	if !foundSession {
		t.Error("tmux new-session was not called")
	}

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

func TestClaudeController_Launch_AddDirs(t *testing.T) {
	ft := newFakeTmux()
	c := claudectrl.NewClaudeControllerWithRunner("key", "localhost", "7070", ft)
	primary := t.TempDir()
	secondary := t.TempDir()
	tertiary := t.TempDir()

	_, err := c.Launch(context.Background(), controller.LaunchOptions{
		SessionID:  "abcd1234-0000-0000-0000-000000000001",
		ProjectDir: primary,
		Prompt:     "test",
		AddDirs:    []string{secondary, "", tertiary}, // empty string should be skipped
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	var newWindow string
	for _, call := range ft.runs {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, "new-window") {
			newWindow = joined
			break
		}
	}
	if newWindow == "" {
		t.Fatal("new-window call not found")
	}
	if !strings.Contains(newWindow, "--add-dir "+secondary) {
		t.Errorf("expected --add-dir for secondary workdir, got: %s", newWindow)
	}
	if !strings.Contains(newWindow, "--add-dir "+tertiary) {
		t.Errorf("expected --add-dir for tertiary workdir, got: %s", newWindow)
	}
	// Empty string must be skipped; there should be no "--add-dir --" sequence.
	if strings.Contains(newWindow, "--add-dir \"\"") || strings.Contains(newWindow, "--add-dir '--") {
		t.Errorf("empty AddDirs entry leaked into argv: %s", newWindow)
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
	if !strings.HasPrefix(handle.TmuxWindow, "feat-dev·") {
		t.Errorf("TmuxWindow = %q, want prefix %q", handle.TmuxWindow, "feat-dev·")
	}
	if !strings.Contains(handle.TmuxWindow, "abcd1234") {
		t.Errorf("TmuxWindow = %q, want short ID %q", handle.TmuxWindow, "abcd1234")
	}
}

