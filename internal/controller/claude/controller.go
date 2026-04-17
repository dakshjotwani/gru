// Package claude is the Claude Code session controller. Launches route
// through env.Environment + persistentpty.PersistentPty so the tmux process
// lives inside the instance (host, container, …) rather than in the Gru
// daemon. That separation is what lets a Gru restart leave running sessions
// alone. See docs/superpowers/specs/2026-04-17-gru-v2-design.md.
package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dakshjotwani/gru/internal/controller"
	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/env/persistentpty"
	"github.com/google/uuid"
)

// ClaudeController launches Claude Code inside an env.Environment instance,
// wrapping the process in a detachable tmux session via PersistentPty.
type ClaudeController struct {
	apiKey    string
	host      string
	port      string
	claudeBin string
	envAdp    env.Environment
	pty       *persistentpty.PersistentPty

	mu   sync.Mutex
	live map[string]env.Instance // sessionID → instance, for Kill lookups
}

// NewClaudeController returns a controller backed by the supplied Environment
// (typically env/host). Panics if e is nil — the controller has no meaningful
// fallback if it can't provision environments.
func NewClaudeController(apiKey, host, port string, e env.Environment) *ClaudeController {
	if e == nil {
		panic("claude: NewClaudeController requires a non-nil env.Environment")
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		bin = "claude" // fall back; let the shell report at launch time
	}
	return &ClaudeController{
		apiKey:    apiKey,
		host:      host,
		port:      port,
		claudeBin: bin,
		envAdp:    e,
		pty:       persistentpty.New(),
		live:      make(map[string]env.Instance),
	}
}

func (c *ClaudeController) RuntimeID() string { return "claude-code" }

func (c *ClaudeController) Capabilities() []controller.Capability {
	return []controller.Capability{controller.CapKill}
}

// shortID truncates a UUID to 8 hex characters for tmux-session naming. The
// full UUID is still the authoritative session ID.
func shortID(sessionID string) string {
	clean := strings.ReplaceAll(sessionID, "-", "")
	if len(clean) >= 8 {
		return clean[:8]
	}
	return clean
}

// tmuxName is the stable tmux-session name for a given Gru session. One tmux
// session per Gru session — a v2 convention departure from v1's one-per-project.
func tmuxName(sessionID string) string {
	return "gru-" + shortID(sessionID)
}

func (c *ClaudeController) Launch(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
	if _, err := os.Stat(opts.ProjectDir); err != nil {
		return nil, fmt.Errorf("claude: project dir: %w", err)
	}

	sessionID := opts.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	// Build the ordered workdir list. env.Host enforces uniqueness on this
	// exact ordered set — first launch wins, duplicates fail loudly.
	workdirs := []string{opts.ProjectDir}
	for _, d := range opts.AddDirs {
		if d == "" {
			continue
		}
		workdirs = append(workdirs, d)
	}

	inst, err := c.envAdp.Create(ctx, env.EnvSpec{
		Name:     sessionID,
		Adapter:  c.envAdp.RuntimeID(),
		Workdirs: workdirs,
	})
	if err != nil {
		return nil, fmt.Errorf("claude: env.Create: %w", err)
	}

	name := tmuxName(sessionID)
	shellCmd := buildClaudeCmd(opts, sessionID, c.apiKey, c.host, c.port, c.claudeBin)
	if err := c.pty.Start(ctx, c.envAdp, inst, name, opts.ProjectDir, shellCmd); err != nil {
		// Best-effort rollback: release the env instance so the workdir-set
		// claim doesn't leak and a retry with the same session ID works.
		_ = c.envAdp.Destroy(context.Background(), inst)
		return nil, fmt.Errorf("claude: persistentpty.Start: %w", err)
	}

	c.mu.Lock()
	c.live[sessionID] = inst
	c.mu.Unlock()

	writeLookupFiles(opts.ProjectDir, sessionID, opts.NoWorktree)

	return &controller.SessionHandle{
		SessionID:   sessionID,
		TmuxSession: name,
		TmuxWindow:  "", // one-window session; callers target the session directly
	}, nil
}

// Kill tears down the tmux session and releases the env instance's resource
// claims. Idempotent — calling Kill on an unknown session returns nil.
func (c *ClaudeController) Kill(ctx context.Context, sessionID string) error {
	c.mu.Lock()
	inst, ok := c.live[sessionID]
	if ok {
		delete(c.live, sessionID)
	}
	c.mu.Unlock()

	// Even if we don't have the instance in memory (e.g. after a server
	// restart), best-effort kill the tmux session by its well-known name so
	// the pane goes away and the user doesn't have to reach for tmux.
	name := tmuxName(sessionID)
	if ok {
		_ = c.pty.Stop(ctx, c.envAdp, inst, name)
		return c.envAdp.Destroy(ctx, inst)
	}
	_ = exec.CommandContext(ctx, "tmux", "kill-session", "-t", name).Run()
	return nil
}

// buildClaudeCmd constructs the shell command string tmux will run as the
// first window's process. Env vars are inlined because tmux 3.0a does not
// support `-e` on new-session.
func buildClaudeCmd(opts controller.LaunchOptions, sessionID, apiKey, host, port, bin string) string {
	var args []string
	if !opts.NoWorktree {
		args = append(args, "--worktree", shortID(sessionID))
	}
	if opts.AutoMode {
		args = append(args, "--permission-mode", "auto")
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Agent != "" {
		args = append(args, "--agent", opts.Agent)
	}
	for _, d := range opts.AddDirs {
		if d == "" {
			continue
		}
		args = append(args, "--add-dir", d)
	}
	if opts.ExtraPrompt != "" {
		args = append(args, "--append-system-prompt", shellQuote(opts.ExtraPrompt))
	}
	if opts.Prompt != "" {
		args = append(args, shellQuote(opts.Prompt))
	}
	return fmt.Sprintf("GRU_SESSION_ID=%s GRU_API_KEY=%s GRU_HOST=%s GRU_PORT=%s %s %s",
		sessionID, apiKey, host, port, bin, strings.Join(args, " "))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// writeLookupFiles emits the per-session files that let gru-hook.sh resolve
// the session ID from its cwd. Best-effort — hook ingestion still works via
// env var fallbacks if any of these writes fail.
func writeLookupFiles(projectDir, sessionID string, noWorktree bool) {
	short := shortID(sessionID)
	dir := filepath.Join(projectDir, ".gru", "sessions")
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(dir, short), []byte(sessionID), 0o644)
	}
	if noWorktree {
		cwdDir := filepath.Join(projectDir, ".gru")
		if err := os.MkdirAll(cwdDir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(cwdDir, "session-id"), []byte(sessionID), 0o644)
		}
	}
}
