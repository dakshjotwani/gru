package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Addr    string         `yaml:"addr"`
	APIKey  string         `yaml:"api_key"`
	DBPath  string         `yaml:"db_path"`
	Journal JournalConfig  `yaml:"journal"`
}

// JournalConfig controls the server-managed journal agent singleton.
// When Enabled is true (the default), the server ensures a journal session is
// running and respawns it if it dies. WorkspaceRoots are the directories the
// journal agent may read to resolve project names into repo paths when Gru has
// no registered project matching a journal entry.
type JournalConfig struct {
	Enabled         *bool    `yaml:"enabled"`
	WorkspaceRoots  []string `yaml:"workspace_roots"`
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
		DBPath: filepath.Join(os.Getenv("HOME"), ".gru", "gru.db"),
		Journal: JournalConfig{
			WorkspaceRoots: []string{filepath.Join(os.Getenv("HOME"), "workspace")},
		},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.APIKey = generateKey()
			if err := cfg.save(path); err != nil {
				return nil, fmt.Errorf("persist config: %w", err)
			}
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	if cfg.APIKey == "" {
		cfg.APIKey = generateKey()
		if err := cfg.save(path); err != nil {
			return nil, fmt.Errorf("persist config: %w", err)
		}
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

func generateKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("config: failed to generate API key: " + err.Error())
	}
	return hex.EncodeToString(b)
}
