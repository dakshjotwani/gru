package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ProjectConfig represents the parsed contents of a project's .gru/config.yaml.
type ProjectConfig struct {
	Project struct {
		Name          string                    `yaml:"name"`
		Runtime       string                    `yaml:"runtime"`
		AgentProfiles map[string]AgentProfile   `yaml:"agent_profiles"`
	} `yaml:"project"`
}

// AgentProfile is a named launch configuration for agent sessions.
type AgentProfile struct {
	Description string   `yaml:"description"`
	Model       string   `yaml:"model"`
	Agent       string   `yaml:"agent"`        // Claude Code agent name (--agent flag)
	ExtraSkills []string `yaml:"extra_skills"` // paths relative to .gru/ dir
	AutoMode    bool     `yaml:"auto_mode"`    // pass --enable-auto-mode to reduce permission prompts
}

// LoadProjectConfig reads .gru/config.yaml from the given project directory.
// Returns a zero-value ProjectConfig (no profiles) if no config file exists.
func LoadProjectConfig(projectDir string) (*ProjectConfig, error) {
	path := filepath.Join(projectDir, ".gru", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &ProjectConfig{}, nil
		}
		return nil, fmt.Errorf("read project config: %w", err)
	}
	var cfg ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse project config: %w", err)
	}
	return &cfg, nil
}

// Profile returns the AgentProfile for the given name.
// Returns a zero-value profile (no error) when name is empty or no config exists.
// Returns an error only when a profile is explicitly requested but the project
// config has profiles and the requested name is not among them.
func (c *ProjectConfig) Profile(name string) (AgentProfile, error) {
	if name == "" {
		return AgentProfile{}, nil
	}
	// No profiles configured → ignore the profile name silently.
	if len(c.Project.AgentProfiles) == 0 {
		return AgentProfile{}, nil
	}
	p, ok := c.Project.AgentProfiles[name]
	if !ok {
		return AgentProfile{}, fmt.Errorf("profile %q not found in project config", name)
	}
	return p, nil
}

// SkillContent reads and returns the content of all extra skill files for this
// profile, resolved relative to the project's .gru/ directory.
func (p *AgentProfile) SkillContent(projectDir string) (string, error) {
	if len(p.ExtraSkills) == 0 {
		return "", nil
	}
	gruDir := filepath.Join(projectDir, ".gru")
	var combined string
	for _, rel := range p.ExtraSkills {
		path := filepath.Join(gruDir, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read skill %q: %w", rel, err)
		}
		combined += string(data) + "\n\n"
	}
	return combined, nil
}
