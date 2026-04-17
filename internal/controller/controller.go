package controller

import (
	"context"
	"fmt"
)

type Capability string

const (
	CapKill          Capability = "kill"
	CapPause         Capability = "pause"
	CapResume        Capability = "resume"
	CapInjectContext Capability = "inject_context"
)

type LaunchOptions struct {
	SessionID   string
	ProjectDir  string
	Prompt      string
	Profile     string
	Model       string   // optional; passed as --model to the agent runtime
	Agent       string   // optional; passed as --agent to select a Claude Code agent
	ExtraPrompt string   // optional extra system prompt content (skills, etc.)
	AutoMode    bool     // pass --enable-auto-mode to use classifier-based auto-approval
	NoWorktree  bool     // skip --worktree; ProjectDir is used as-is (non-git dirs like the journal)
	// AddDirs is an ordered list of extra workdirs beyond ProjectDir. Each is
	// passed to Claude Code as --add-dir <path> so the agent can read/edit
	// files in secondary repos (kernel + uboot + buildroot, backend + infra,
	// etc.). The primary cwd remains ProjectDir (v2 spec §Project).
	AddDirs []string
	Env     map[string]string
}

type SessionHandle struct {
	SessionID   string
	TmuxSession string
	TmuxWindow  string
}

type SessionController interface {
	RuntimeID() string
	Capabilities() []Capability
	Launch(ctx context.Context, opts LaunchOptions) (*SessionHandle, error)
}

type Registry struct {
	controllers map[string]SessionController
}

func NewRegistry() *Registry {
	return &Registry{controllers: make(map[string]SessionController)}
}

func (r *Registry) Register(c SessionController) {
	id := c.RuntimeID()
	if _, exists := r.controllers[id]; exists {
		panic(fmt.Sprintf("controller: duplicate registration for runtime %q", id))
	}
	r.controllers[id] = c
}

func (r *Registry) Get(runtimeID string) (SessionController, error) {
	c, ok := r.controllers[runtimeID]
	if !ok {
		return nil, fmt.Errorf("controller: no controller registered for runtime %q", runtimeID)
	}
	return c, nil
}
