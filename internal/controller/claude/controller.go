package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

	logDir := filepath.Join(os.Getenv("HOME"), ".gru", "sessions")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return nil, fmt.Errorf("claude: create session log dir: %w", err)
	}
	logFile := filepath.Join(logDir, sessionID+".log")

	// Tee output to a log file so it survives after the window closes.
	// remain-on-exit keeps the pane visible for inspection with gru attach.
	claudeCmd := fmt.Sprintf("%s --dangerously-skip-permissions -p '%s' 2>&1 | tee %s",
		c.claudeBin, opts.Prompt, logFile)

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
	// Set remain-on-exit on the specific window so the pane stays visible
	// after the command finishes. Must target the window directly — setting
	// this at the session level does not propagate to new windows in tmux.
	windowTarget := tmuxSession + ":" + windowName
	_ = c.tmux.Run("set-window-option", "-t", windowTarget, "remain-on-exit", "on")

	done := make(chan struct{})

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
