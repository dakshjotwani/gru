package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Addr      string          `yaml:"addr"`
	Bind      string          `yaml:"bind"` // "tailnet" (default), "loopback", "all"
	DBPath    string          `yaml:"db_path"`
	Journal   JournalConfig   `yaml:"journal"`
	Attention AttentionConfig `yaml:"attention"`
}

// AttentionConfig tunes the attention-score engine (weights only for v2).
// Any zero field falls back to the default documented in the v2 spec.
type AttentionConfig struct {
	Weights AttentionWeights `yaml:"weights"`
}

// AttentionWeights overrides individual signal weights. See
// internal/attention.DefaultWeights for the default values.
type AttentionWeights struct {
	Paused         float64 `yaml:"paused"`
	Notification   float64 `yaml:"notification"`
	ToolError      float64 `yaml:"tool_error"`
	StalenessCap   float64 `yaml:"staleness_cap"`
	StalenessStart string  `yaml:"staleness_start"` // duration string, e.g. "5m"
	StalenessFull  string  `yaml:"staleness_full"`  // duration string, e.g. "15m"
}

// ParseStalenessDurations decodes the two duration strings. Returns zero
// duration for an empty string, letting the engine fall back to its default.
func (a AttentionWeights) ParseStalenessDurations() (start, full time.Duration, err error) {
	if a.StalenessStart != "" {
		start, err = time.ParseDuration(a.StalenessStart)
		if err != nil {
			return 0, 0, fmt.Errorf("attention.weights.staleness_start: %w", err)
		}
	}
	if a.StalenessFull != "" {
		full, err = time.ParseDuration(a.StalenessFull)
		if err != nil {
			return 0, 0, fmt.Errorf("attention.weights.staleness_full: %w", err)
		}
	}
	return start, full, nil
}

// JournalConfig controls the server-managed journal agent singleton.
// When Enabled is true (the default), the server ensures a journal session is
// running and respawns it if it dies. WorkspaceRoots are the directories the
// journal agent may read to resolve project names into repo paths when Gru has
// no registered project matching a journal entry.
type JournalConfig struct {
	Enabled        *bool    `yaml:"enabled"`
	WorkspaceRoots []string `yaml:"workspace_roots"`
}

// IsEnabled returns true unless explicitly set to false. Missing field = enabled.
func (j JournalConfig) IsEnabled() bool {
	if j.Enabled == nil {
		return true
	}
	return *j.Enabled
}

// Load reads server config from path. Missing file returns defaults;
// parse errors are returned as errors.
func Load(path string) (*Config, error) {
	cfg := &Config{
		Addr:   ":7777",
		Bind:   "tailnet",
		DBPath: filepath.Join(os.Getenv("HOME"), ".gru", "gru.db"),
		Journal: JournalConfig{
			WorkspaceRoots: []string{filepath.Join(os.Getenv("HOME"), "workspace")},
		},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := cfg.save(path); err != nil {
				return nil, fmt.Errorf("persist config: %w", err)
			}
			return cfg, nil
		}
		return nil, err
	}

	// Warn if the old api_key field is present — it's no longer read.
	// (The server now binds to the tailnet interface and has no auth.)
	if strings.Contains(string(data), "\napi_key:") || strings.HasPrefix(string(data), "api_key:") {
		fmt.Fprintln(os.Stderr, "gru: server.yaml contains api_key — ignored (bearer auth removed; tailnet bind is the boundary)")
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	if cfg.Bind == "" {
		cfg.Bind = "tailnet"
	}

	return cfg, nil
}

func (c *Config) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
