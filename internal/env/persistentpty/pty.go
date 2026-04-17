// Package persistentpty wraps any Environment with a detachable tmux session
// keyed by a stable name. This is the seam that lets Gru reattach to a running
// agent across Gru restarts — the tmux process lives inside the env, not in
// the Gru daemon, so SIGHUP from the daemon exiting doesn't kill the agent.
package persistentpty

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dakshjotwani/gru/internal/env"
)

// PersistentPty manages named tmux sessions on top of an Environment. All
// operations route through Environment.Exec() / ExecPty() so the same logic
// works against the host, a container, or a user-supplied command env.
type PersistentPty struct{}

func New() *PersistentPty { return &PersistentPty{} }

// Status describes whether a named pty session exists.
type Status struct {
	Exists bool
}

// ValidateName returns an error if name contains characters that tmux treats
// specially in target selectors (`.`, `:`). Callers should mint names from
// UUIDs or limited alphabets; this check catches programming errors.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("persistentpty: empty session name")
	}
	for _, c := range name {
		if c == '.' || c == ':' {
			return fmt.Errorf("persistentpty: session name %q contains reserved character %q", name, string(c))
		}
	}
	return nil
}

// Start creates a new tmux session named `name`, sets working dir to `workdir`
// (if non-empty), and launches `shellCmd` as the session's first window. The
// shellCmd is passed as a single argument to tmux, which interprets it as a
// shell command line. Caller is responsible for quoting.
//
// Returns nil if the tmux session already exists (idempotent).
func (p *PersistentPty) Start(ctx context.Context, e env.Environment, inst env.Instance, name, workdir, shellCmd string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if !hasTmux(inst) {
		return fmt.Errorf("persistentpty: instance has no tmux in PtyHolders (%v)", inst.PtyHolders)
	}
	st, err := p.Status(ctx, e, inst, name)
	if err != nil {
		return err
	}
	if st.Exists {
		return nil
	}
	args := []string{"tmux", "new-session", "-d", "-s", name}
	if workdir != "" {
		args = append(args, "-c", workdir)
	}
	if shellCmd != "" {
		args = append(args, shellCmd)
	}
	res, err := e.Exec(ctx, inst, args)
	if err != nil {
		return fmt.Errorf("persistentpty: tmux new-session: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("persistentpty: tmux new-session exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	// Make the pane stick around after the command exits so the user can
	// still attach and read final output. Set on the created window
	// specifically — the session-level option doesn't propagate.
	_, _ = e.Exec(ctx, inst, []string{"tmux", "set-window-option", "-t", name, "remain-on-exit", "on"})
	return nil
}

// Attach returns a pty-backed stream to `tmux attach-session -t name`. Close
// the returned handle to detach (the tmux session keeps running).
func (p *PersistentPty) Attach(ctx context.Context, e env.Environment, inst env.Instance, name string) (io.ReadWriteCloser, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if !hasTmux(inst) {
		return nil, fmt.Errorf("persistentpty: instance has no tmux in PtyHolders")
	}
	return e.ExecPty(ctx, inst, []string{"tmux", "attach-session", "-t", name})
}

// Status reports whether the named tmux session exists.
func (p *PersistentPty) Status(ctx context.Context, e env.Environment, inst env.Instance, name string) (Status, error) {
	if err := ValidateName(name); err != nil {
		return Status{}, err
	}
	if !hasTmux(inst) {
		return Status{}, fmt.Errorf("persistentpty: instance has no tmux in PtyHolders")
	}
	res, err := e.Exec(ctx, inst, []string{"tmux", "has-session", "-t", name})
	if err != nil {
		return Status{}, err
	}
	return Status{Exists: res.ExitCode == 0}, nil
}

// Stop kills the named tmux session. Idempotent — returns nil if the session
// doesn't exist.
func (p *PersistentPty) Stop(ctx context.Context, e env.Environment, inst env.Instance, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if !hasTmux(inst) {
		return fmt.Errorf("persistentpty: instance has no tmux in PtyHolders")
	}
	res, err := e.Exec(ctx, inst, []string{"tmux", "kill-session", "-t", name})
	if err != nil {
		return err
	}
	// tmux returns non-zero when the session doesn't exist. Treat that as
	// success — Stop is idempotent.
	if res.ExitCode != 0 {
		stderr := strings.ToLower(strings.TrimSpace(string(res.Stderr)))
		if strings.Contains(stderr, "can't find session") || strings.Contains(stderr, "no current session") || strings.Contains(stderr, "session not found") {
			return nil
		}
		return fmt.Errorf("persistentpty: tmux kill-session exit %d: %s", res.ExitCode, stderr)
	}
	return nil
}

func hasTmux(inst env.Instance) bool {
	for _, h := range inst.PtyHolders {
		if h == "tmux" {
			return true
		}
	}
	return false
}
