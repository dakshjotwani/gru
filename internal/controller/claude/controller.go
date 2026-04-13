package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dakshjotwani/gru/internal/controller"
	"github.com/google/uuid"
)

type tmuxRunner interface {
	Run(args ...string) error
	Output(args ...string) ([]byte, error)
}

type realTmux struct{}

func (r *realTmux) Run(args ...string) error {
	return exec.Command("tmux", args...).Run()
}

func (r *realTmux) Output(args ...string) ([]byte, error) {
	return exec.Command("tmux", args...).Output()
}

type ClaudeController struct {
	apiKey    string
	host      string
	port      string
	claudeBin string
	tmux      tmuxRunner
}

func NewClaudeController(apiKey, host, port string) *ClaudeController {
	bin, err := exec.LookPath("claude")
	if err != nil {
		bin = "claude" // fall back and let the shell report the error at launch time
	}
	return &ClaudeController{apiKey: apiKey, host: host, port: port, claudeBin: bin, tmux: &realTmux{}}
}

func NewClaudeControllerWithRunner(apiKey, host, port string, runner tmuxRunner) *ClaudeController {
	return &ClaudeController{apiKey: apiKey, host: host, port: port, claudeBin: "claude", tmux: runner}
}

func (c *ClaudeController) RuntimeID() string { return "claude-code" }

func (c *ClaudeController) Capabilities() []controller.Capability {
	return []controller.Capability{controller.CapKill}
}

func sanitizeProjectName(name string) string {
	name = strings.ToLower(name)
	replacer := strings.NewReplacer("/", "-", " ", "-", ".", "-")
	name = replacer.Replace(name)
	name = strings.TrimPrefix(name, "gru-")
	return name
}

func (c *ClaudeController) Launch(ctx context.Context, opts controller.LaunchOptions) (*controller.SessionHandle, error) {
	if _, err := os.Stat(opts.ProjectDir); err != nil {
		return nil, fmt.Errorf("claude: project dir: %w", err)
	}

	sessionID := opts.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	projectName := sanitizeProjectName(opts.ProjectDir)
	tmuxSession := "gru-" + projectName

	if err := c.tmux.Run("new-session", "-d", "-s", tmuxSession); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if !strings.Contains(string(exitErr.Stderr), "duplicate session") {
				_ = err
			}
		}
	}

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

	// Launch claude interactively. Passing the prompt as a positional argument
	// starts the session with that task; claude remains interactive afterward.
	// --worktree <shortID> tells Claude Code to create/reuse a worktree at
	// <projectDir>/.claude/worktrees/<shortID>, preserving memories across sessions.
	claudeArgs := []string{"--worktree", shortID}
	if opts.AutoMode {
		claudeArgs = append(claudeArgs, "--permission-mode", "auto")
	}
	if opts.Model != "" {
		claudeArgs = append(claudeArgs, "--model", opts.Model)
	}
	if opts.ExtraPrompt != "" {
		escaped := "'" + strings.ReplaceAll(opts.ExtraPrompt, "'", "'\\''") + "'"
		claudeArgs = append(claudeArgs, "--append-system-prompt", escaped)
	}
	if opts.Prompt != "" {
		// Shell-quote the prompt using single quotes so spaces and special
		// characters are passed through to claude verbatim.
		escaped := "'" + strings.ReplaceAll(opts.Prompt, "'", "'\\''") + "'"
		claudeArgs = append(claudeArgs, escaped)
	}
	// Inline env vars in the command string — tmux 3.0a does not support -e on new-window.
	claudeCmd := fmt.Sprintf("GRU_SESSION_ID=%s GRU_API_KEY=%s GRU_HOST=%s GRU_PORT=%s %s %s",
		sessionID, c.apiKey, c.host, c.port,
		c.claudeBin, strings.Join(claudeArgs, " "))

	newWindowArgs := []string{
		"new-window",
		"-t", tmuxSession,
		"-n", windowName,
		"-c", opts.ProjectDir,
		claudeCmd,
	}
	if err := c.tmux.Run(newWindowArgs...); err != nil {
		return nil, fmt.Errorf("claude: tmux new-window: %w", err)
	}

	// Write a lookup file so the hook script can resolve the GRU session ID
	// from the worktree CWD without relying on environment variables (Claude
	// Code sanitizes the hook subprocess environment).
	sessionLookupDir := filepath.Join(opts.ProjectDir, ".gru", "sessions")
	if err := os.MkdirAll(sessionLookupDir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(sessionLookupDir, shortID), []byte(sessionID), 0o644)
	}

	// Set remain-on-exit on the specific window so the pane stays visible
	// after the command finishes. Must target the window directly — setting
	// this at the session level does not propagate to new windows in tmux.
	windowTarget := tmuxSession + ":" + windowName
	_ = c.tmux.Run("set-window-option", "-t", windowTarget, "remain-on-exit", "on")

	return &controller.SessionHandle{
		SessionID:   sessionID,
		TmuxSession: tmuxSession,
		TmuxWindow:  windowName,
	}, nil
}
