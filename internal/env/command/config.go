// Package command implements the "command" Environment adapter: a
// template-driven escape hatch for wrapping existing user infrastructure
// (Dockerfiles, docker-compose, bespoke scripts, etc.) behind the Gru
// Environment contract.
//
// The operator supplies six shell command templates (create, exec, exec_pty,
// destroy, events, status). Gru renders them with session/project-scoped
// variables and runs them verbatim. See docs/superpowers/specs/2026-04-17-gru-v2-design.md
// §The `command` adapter for the full contract.
package command

import (
	"encoding/json"
	"fmt"
)

// Config is the EnvSpec.Config payload the command adapter expects.
type Config struct {
	// Create is the shell command template Gru runs on Create(). Must print
	// a single JSON object on the last non-empty stdout line with at least
	// {"provider_ref": "..."} and optionally {"pty_holders": [...]}.
	// Hard-killed after CreateTimeout.
	Create string `json:"create"`

	// Exec is the template for one-shot non-interactive commands. The
	// rendered command is invoked with the agent's argv appended as
	// additional shell arguments.
	Exec string `json:"exec"`

	// ExecPty is the template for pty-backed commands. The rendered command
	// MUST set up a real pty (via script(1), socat, docker exec -it, etc.)
	// and exec the given argv inside it.
	ExecPty string `json:"exec_pty"`

	// Destroy is the template for teardown. MUST be idempotent — may be
	// called with provider_ref="" after a failed Create.
	Destroy string `json:"destroy"`

	// Events is a long-lived command; every stdout line is a JSON Event.
	// Must emit {"kind":"heartbeat"} at least every 60s of silence.
	Events string `json:"events"`

	// Status is a one-shot command that prints a single JSON Status object.
	Status string `json:"status"`
}

// parseConfig extracts a Config from EnvSpec.Config. Accepts either a
// typed Config already decoded, or a generic map[string]any from a JSON /
// YAML round-trip.
func parseConfig(raw map[string]any) (Config, error) {
	if raw == nil {
		return Config{}, fmt.Errorf("command: missing config")
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return Config{}, fmt.Errorf("command: re-marshal config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(buf, &cfg); err != nil {
		return Config{}, fmt.Errorf("command: decode config: %w", err)
	}
	if cfg.Create == "" {
		return Config{}, fmt.Errorf("command: config.create is required")
	}
	if cfg.Destroy == "" {
		return Config{}, fmt.Errorf("command: config.destroy is required")
	}
	if cfg.Exec == "" {
		return Config{}, fmt.Errorf("command: config.exec is required")
	}
	if cfg.ExecPty == "" {
		return Config{}, fmt.Errorf("command: config.exec_pty is required")
	}
	// events and status are optional. A spec without events still works —
	// the adapter just returns a closed channel, and Status falls back to
	// a synthetic "probably running" until first event arrives.
	return cfg, nil
}
