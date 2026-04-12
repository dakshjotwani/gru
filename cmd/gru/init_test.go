package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunInit_CreatesHookScriptAndSettings(t *testing.T) {
	projectDir := t.TempDir()

	// Point hookSrcPath to the actual hook script in the repo root.
	// In tests we override via the package-level variable hookScriptSrc.
	origSrc := hookScriptSrc
	hookScriptSrc = filepath.Join(repoRoot(), "hooks", "claude-code.sh")
	defer func() { hookScriptSrc = origSrc }()

	if err := runInit([]string{projectDir}); err != nil {
		t.Fatalf("runInit error: %v", err)
	}

	// Hook script must be copied.
	hookDst := filepath.Join(projectDir, ".gru", "hooks", "gru-hook.sh")
	info, err := os.Stat(hookDst)
	if err != nil {
		t.Fatalf("hook script not created at %s: %v", hookDst, err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("hook script must be executable")
	}

	// settings.json must exist and contain all expected hook keys.
	settingsPath := filepath.Join(projectDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings.json not valid JSON: %v", err)
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("settings.json missing 'hooks' object")
	}

	expected := []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "Notification", "Stop", "SubagentStart", "SubagentStop"}
	for _, key := range expected {
		if _, exists := hooks[key]; !exists {
			t.Errorf("hooks missing key %q", key)
		}
	}
}

func TestRunInit_MergesExistingSettings(t *testing.T) {
	projectDir := t.TempDir()

	// Pre-create a settings.json with existing keys.
	claudeDir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"theme":"dark","hooks":{"PreToolUse":[{"matcher":"","hooks":[{"type":"command","command":"/old/hook.sh"}]}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	origSrc := hookScriptSrc
	hookScriptSrc = filepath.Join(repoRoot(), "hooks", "claude-code.sh")
	defer func() { hookScriptSrc = origSrc }()

	if err := runInit([]string{projectDir}); err != nil {
		t.Fatalf("runInit error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(projectDir, ".claude", "settings.json"))
	var settings map[string]interface{}
	json.Unmarshal(data, &settings)

	// Existing non-hook key must be preserved.
	if settings["theme"] != "dark" {
		t.Errorf("existing 'theme' key was lost; settings: %s", data)
	}

	hooks := settings["hooks"].(map[string]interface{})
	// All 7 hook types must be present.
	for _, key := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "Notification", "Stop", "SubagentStart", "SubagentStop"} {
		if _, exists := hooks[key]; !exists {
			t.Errorf("hooks missing key %q after merge", key)
		}
	}
}

func TestRunInit_MissingArgument(t *testing.T) {
	if err := runInit([]string{}); err == nil {
		t.Fatal("expected error when no project dir given, got nil")
	}
}

// repoRoot walks up from the test binary location to find the module root.
// Works because go test sets the working directory to the package directory.
func repoRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
