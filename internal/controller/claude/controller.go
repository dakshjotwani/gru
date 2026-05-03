// Package claude is the Claude Code session controller. Launches route
// through env.Environment + persistentpty.PersistentPty so the tmux process
// lives inside the instance (host, container, …) rather than in the Gru
// daemon. That separation is what lets a Gru restart leave running sessions
// alone. See docs/superpowers/specs/2026-04-17-gru-v2-design.md.
package claude

import (
	"context"
	_ "embed"
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

// minionPrompt is appended to every Claude minion's system prompt so the
// session knows it is gru-managed and which CLI commands surface things to
// the operator's dashboard. The assistant (Profile=="journal") has its own
// dedicated prompt and skips this blurb.
//
//go:embed minion_prompt.md
var minionPrompt string

// ClaudeController launches Claude Code inside an env.Environment instance,
// wrapping the process in a detachable tmux session via PersistentPty.
//
// Adapter selection is per-launch: if LaunchOptions.EnvSpec is set, the
// controller resolves the adapter from the registry by EnvSpec.Adapter;
// otherwise it uses defaultAdapter (typically "host"). Each live session
// remembers which adapter provisioned it so Kill can tear down correctly.
type ClaudeController struct {
	host           string
	port           string
	claudeBin      string
	envs           *env.Registry
	defaultAdapter string
	pty            *persistentpty.PersistentPty

	mu   sync.Mutex
	live map[string]liveSession // sessionID → (adapter, instance), for Kill lookups
}

// liveSession pairs an Instance with the Environment that created it, so Kill
// knows which adapter's Destroy to call. A sessionID is keyed to exactly one
// (Environment, Instance) pair for its lifetime.
type liveSession struct {
	adapter env.Environment
	inst    env.Instance
}

// NewClaudeController returns a controller backed by a registry of env
// adapters. defaultAdapter is the runtime ID used when a launch doesn't
// specify its own EnvSpec. Panics if registry is nil or defaultAdapter is
// not registered — a controller with no provisioning substrate is useless.
func NewClaudeController(host, port string, envs *env.Registry, defaultAdapter string) *ClaudeController {
	if envs == nil {
		panic("claude: NewClaudeController requires a non-nil env.Registry")
	}
	if _, err := envs.Get(defaultAdapter); err != nil {
		panic(fmt.Sprintf("claude: defaultAdapter %q not in registry: %v", defaultAdapter, err))
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		// launchd's PATH (/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin)
		// omits ~/.local/bin where claude is typically installed. If PATH
		// search fails, probe the well-known install location so launches
		// don't silently fail with "command not found" inside the tmux pane.
		if home, herr := os.UserHomeDir(); herr == nil {
			candidate := filepath.Join(home, ".local", "bin", "claude")
			if _, serr := os.Stat(candidate); serr == nil {
				bin = candidate
			}
		}
		if bin == "" {
			bin = "claude"
		}
	}
	return &ClaudeController{
		host:           host,
		port:           port,
		claudeBin:      bin,
		envs:           envs,
		defaultAdapter: defaultAdapter,
		pty:            persistentpty.New(),
		live:           make(map[string]liveSession),
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
	if len(opts.EnvSpec.Workdirs) == 0 {
		return nil, fmt.Errorf("claude: env spec has no workdirs")
	}
	if opts.EnvSpec.Adapter == "" {
		return nil, fmt.Errorf("claude: env spec has no adapter")
	}

	sessionID := opts.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	adapter, err := c.envs.Get(opts.EnvSpec.Adapter)
	if err != nil {
		return nil, fmt.Errorf("claude: env adapter %q: %w", opts.EnvSpec.Adapter, err)
	}

	// Take the spec verbatim; override only Name (session id lives in the
	// controller, not in the spec file).
	spec := opts.EnvSpec
	spec.Name = sessionID

	primaryWorkdir := spec.Workdirs[0]
	if _, err := os.Stat(primaryWorkdir); err != nil {
		return nil, fmt.Errorf("claude: primary workdir %q: %w", primaryWorkdir, err)
	}

	inst, err := adapter.Create(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("claude: env.Create (%s): %w", spec.Adapter, err)
	}

	// Register the live session BEFORE starting the pty. If a concurrent
	// Kill(sessionID) arrives in the middle of launch, it needs to find the
	// adapter/instance pair so it can Destroy them — not fall through to the
	// bare tmux-kill-session branch and leak the env's resource claim.
	c.mu.Lock()
	c.live[sessionID] = liveSession{adapter: adapter, inst: inst}
	c.mu.Unlock()

	// Ask the adapter for per-launch flags (--worktree when opted in) and
	// optional cwd override. Host with worktree=true returns ["--worktree",
	// <shortID>]; command returns nothing by default.
	agentArgs, err := adapter.AgentArgs(ctx, inst)
	if err != nil {
		c.mu.Lock()
		delete(c.live, sessionID)
		c.mu.Unlock()
		_ = adapter.Destroy(context.Background(), inst)
		return nil, fmt.Errorf("claude: env.AgentArgs: %w", err)
	}
	agentCwd := agentArgs.Cwd
	if agentCwd == "" {
		agentCwd = primaryWorkdir
	}

	// --add-dir <each extra workdir>. The spec's Workdirs[0] is the cwd;
	// Workdirs[1..] become --add-dir flags for Claude Code.
	addDirArgs := make([]string, 0, 2*(len(spec.Workdirs)-1))
	for _, d := range spec.Workdirs[1:] {
		if d == "" {
			continue
		}
		addDirArgs = append(addDirArgs, "--add-dir", d)
	}

	name := tmuxName(sessionID)
	shellCmd := buildClaudeCmd(opts, sessionID, c.host, c.port, c.claudeBin, agentArgs.ExtraArgs, addDirArgs)
	if err := c.pty.Start(ctx, adapter, inst, name, agentCwd, shellCmd); err != nil {
		// Best-effort rollback.
		c.mu.Lock()
		delete(c.live, sessionID)
		c.mu.Unlock()
		_ = adapter.Destroy(context.Background(), inst)
		return nil, fmt.Errorf("claude: persistentpty.Start: %w", err)
	}

	writeLookupFiles(primaryWorkdir, sessionID, !containsWorktreeFlag(agentArgs.ExtraArgs))

	return &controller.SessionHandle{
		SessionID:   sessionID,
		TmuxSession: name,
		TmuxWindow:  "",
	}, nil
}

// containsWorktreeFlag reports whether --worktree is in the adapter's
// extra args. Used only to decide whether writeLookupFiles should also
// drop a cwd-based session-id file (for the non-worktree path). This is
// a file-placement heuristic, not a launch-behavior switch.
func containsWorktreeFlag(args []string) bool {
	for _, a := range args {
		if a == "--worktree" {
			return true
		}
	}
	return false
}

// Kill tears down the tmux session and releases the env instance's resource
// claims via the same adapter that created it. Idempotent — calling Kill on
// an unknown session returns nil.
func (c *ClaudeController) Kill(ctx context.Context, sessionID string) error {
	c.mu.Lock()
	ls, ok := c.live[sessionID]
	if ok {
		delete(c.live, sessionID)
	}
	c.mu.Unlock()

	// Even if we don't have the instance in memory (e.g. after a server
	// restart), best-effort kill the tmux session by its well-known name so
	// the pane goes away and the user doesn't have to reach for tmux.
	name := tmuxName(sessionID)
	if ok {
		_ = c.pty.Stop(ctx, ls.adapter, ls.inst, name)
		return ls.adapter.Destroy(ctx, ls.inst)
	}
	_ = exec.CommandContext(ctx, "tmux", "kill-session", "-t", name).Run()
	return nil
}

// buildClaudeCmd constructs the shell command string tmux will run as the
// first window's process. Env vars are inlined because tmux 3.0a does not
// support `-e` on new-session. Flag order: adapter-provided extras
// (--worktree, etc.) first, then base flags, then --add-dirs, then prompt.
func buildClaudeCmd(
	opts controller.LaunchOptions,
	sessionID, host, port, bin string,
	adapterArgs, addDirArgs []string,
) string {
	var args []string
	args = append(args, adapterArgs...)
	// Pin Claude's session id to the gru session id. Without this,
	// Claude generates its own UUID and writes the transcript to
	// ~/.claude/projects/<encoded-cwd>/<random-uuid>.jsonl — which
	// the tailer cannot match back to a gru session, so multiple gru
	// sessions sharing a project dir all resolve to the same
	// most-recently-modified transcript and their statuses pollute
	// each other. With --session-id, the transcript is named
	// <gru-session-id>.jsonl and deriveTranscriptPath finds it
	// deterministically.
	args = append(args, "--session-id", sessionID)
	if opts.AutoMode {
		args = append(args, "--permission-mode", "auto")
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Agent != "" {
		args = append(args, "--agent", opts.Agent)
	}
	args = append(args, addDirArgs...)
	if extra := composeExtraPrompt(opts); extra != "" {
		args = append(args, "--append-system-prompt", shellQuote(extra))
	}
	if opts.Prompt != "" {
		args = append(args, shellQuote(opts.Prompt))
	}
	// Propagate GRU_STATE_DIR so the launched session's CLI helpers
	// (gru artifact add, gru link add) resolve server.yaml from the
	// same state dir this server was started with — important when
	// the operator runs a non-default test instance alongside their
	// primary one. tmux scrubs env on new-session, so we must set it
	// explicitly on the command line.
	stateDir := ""
	if d := os.Getenv("GRU_STATE_DIR"); d != "" {
		stateDir = "GRU_STATE_DIR=" + d + " "
	}
	return fmt.Sprintf("%sGRU_SESSION_ID=%s GRU_HOST=%s GRU_PORT=%s %s %s",
		stateDir, sessionID, host, port, bin, strings.Join(args, " "))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// composeExtraPrompt returns the full --append-system-prompt content for a
// launch. Minions get the gru-awareness blurb prepended to whatever the
// caller passed (typically per-profile skill content). The journal/assistant
// has its own complete prompt and is left alone.
func composeExtraPrompt(opts controller.LaunchOptions) string {
	if opts.Profile == "journal" {
		return opts.ExtraPrompt
	}
	if opts.ExtraPrompt == "" {
		return minionPrompt
	}
	return minionPrompt + "\n\n" + opts.ExtraPrompt
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
