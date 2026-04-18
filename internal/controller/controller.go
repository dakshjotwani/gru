package controller

import (
	"context"
	"fmt"

	"github.com/dakshjotwani/gru/internal/env"
)

type Capability string

const (
	CapKill          Capability = "kill"
	CapPause         Capability = "pause"
	CapResume        Capability = "resume"
	CapInjectContext Capability = "inject_context"
)

// LaunchOptions is the controller-level payload for a session launch. The
// env spec is load-bearing: it declares the adapter, the workdirs the agent
// will see, and any adapter-specific config (worktree on/off, custom
// mounts, command templates). The controller passes it verbatim to
// env.Environment.Create — no workdir override, no add-dir merging.
type LaunchOptions struct {
	SessionID   string
	Prompt      string
	Profile     string
	Model       string // optional; passed as --model to the agent runtime
	Agent       string // optional; passed as --agent to select a Claude Code agent
	ExtraPrompt string // optional extra system prompt content (skills, etc.)
	AutoMode    bool   // pass --enable-auto-mode to use classifier-based auto-approval
	Env         map[string]string

	// EnvSpec carries the adapter name, Workdirs, and Config for this launch.
	// Required — the controller does not synthesize a default spec.
	EnvSpec env.EnvSpec
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

// Killer is the optional interface a SessionController may implement to
// participate in KillSession. Implementations are responsible for tearing
// down the underlying process (tmux, container, etc.) and releasing any
// adapter-level resource claims (e.g. env.Host workdir-set uniqueness).
// Server code should do a type assertion: if the controller does not
// implement Killer, KillSession falls back to purely database-level cleanup.
type Killer interface {
	Kill(ctx context.Context, sessionID string) error
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
