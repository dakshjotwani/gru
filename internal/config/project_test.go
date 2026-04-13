package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProjectConfig_missing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatalf("expected no error for missing config, got: %v", err)
	}
	if len(cfg.Project.AgentProfiles) != 0 {
		t.Errorf("expected no profiles, got %d", len(cfg.Project.AgentProfiles))
	}
}

func TestLoadProjectConfig_basicProfiles(t *testing.T) {
	dir := t.TempDir()
	gruDir := filepath.Join(dir, ".gru")
	if err := os.MkdirAll(gruDir, 0o755); err != nil {
		t.Fatal(err)
	}

	yaml := `
project:
  name: my-project
  agent_profiles:
    feature-dev:
      description: "Implement new features"
      model: claude-sonnet-4-6
    bug-fix:
      description: "Debug and fix issues"
      model: claude-opus-4-6
`
	if err := os.WriteFile(filepath.Join(gruDir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Project.Name != "my-project" {
		t.Errorf("expected name %q, got %q", "my-project", cfg.Project.Name)
	}
	if len(cfg.Project.AgentProfiles) != 2 {
		t.Errorf("expected 2 profiles, got %d", len(cfg.Project.AgentProfiles))
	}
	p := cfg.Project.AgentProfiles["feature-dev"]
	if p.Model != "claude-sonnet-4-6" {
		t.Errorf("expected model %q, got %q", "claude-sonnet-4-6", p.Model)
	}
}

func TestProjectConfig_Profile(t *testing.T) {
	cfg := &ProjectConfig{}
	cfg.Project.AgentProfiles = map[string]AgentProfile{
		"bugfix": {Model: "claude-opus-4-6", Description: "Debug"},
	}

	// Empty name → zero value, no error.
	p, err := cfg.Profile("")
	if err != nil {
		t.Errorf("empty name: unexpected error: %v", err)
	}
	if p.Model != "" {
		t.Errorf("empty name: expected empty model, got %q", p.Model)
	}

	// Known profile.
	p, err = cfg.Profile("bugfix")
	if err != nil {
		t.Errorf("known profile: unexpected error: %v", err)
	}
	if p.Model != "claude-opus-4-6" {
		t.Errorf("known profile: expected model %q, got %q", "claude-opus-4-6", p.Model)
	}

	// Unknown profile when profiles exist → error.
	_, err = cfg.Profile("nonexistent")
	if err == nil {
		t.Error("nonexistent profile: expected error, got nil")
	}

	// Unknown profile when NO profiles configured → no error.
	empty := &ProjectConfig{}
	_, err = empty.Profile("any")
	if err != nil {
		t.Errorf("no profiles configured: expected no error, got: %v", err)
	}
}

func TestAgentProfile_SkillContent(t *testing.T) {
	dir := t.TempDir()
	gruDir := filepath.Join(dir, ".gru", "skills")
	if err := os.MkdirAll(gruDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gruDir, "workflow.md"), []byte("# Workflow\nDo things."), 0o644); err != nil {
		t.Fatal(err)
	}

	p := AgentProfile{ExtraSkills: []string{"skills/workflow.md"}}
	content, err := p.SkillContent(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content == "" {
		t.Error("expected non-empty skill content")
	}
}
