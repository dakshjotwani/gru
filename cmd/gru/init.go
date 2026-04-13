package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// hookScriptSrc is the path to the hook script to copy.
// Overridable in tests.
var hookScriptSrc = func() string {
	// Default: resolve relative to the binary's location using the module root convention.
	// At runtime the binary is built from the repo root; at test time repoRoot() is used.
	dir, _ := os.Executable()
	root := filepath.Dir(filepath.Dir(dir)) // binary lives in bin/, go up twice
	candidate := filepath.Join(root, "hooks", "claude-code.sh")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	// Fallback: relative to working directory (useful when running directly with `go run`).
	return filepath.Join("hooks", "claude-code.sh")
}()

// hookTypes lists every Claude Code hook event that Gru intercepts.
var hookTypes = []string{
	"PreToolUse",
	"PostToolUse",
	"PostToolUseFailure",
	"Notification",
	"Stop",
	"SubagentStart",
	"SubagentStop",
}

// runInit implements the `gru init <project-dir>` subcommand.
func runInit(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gru init <project-dir>")
	}
	projectDir := args[0]

	// 1. Copy hook script to <project-dir>/.gru/hooks/gru-hook.sh
	hookDst := filepath.Join(projectDir, ".gru", "hooks", "gru-hook.sh")
	if err := os.MkdirAll(filepath.Dir(hookDst), 0o755); err != nil {
		return fmt.Errorf("create .gru/hooks dir: %w", err)
	}
	if err := copyFile(hookScriptSrc, hookDst, 0o755); err != nil {
		return fmt.Errorf("copy hook script: %w", err)
	}

	// 2. Read or initialise .claude/settings.json
	settingsDir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")

	settings := map[string]interface{}{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err2 := json.Unmarshal(data, &settings); err2 != nil {
			return fmt.Errorf("parse existing settings.json: %w", err2)
		}
	}

	// 3. Merge hook entries — one entry per hook type, matcher "" catches all tools.
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}

	hookEntry := map[string]interface{}{
		"type":    "command",
		"command": hookDst,
	}
	hookBlock := []interface{}{
		map[string]interface{}{
			"matcher": "",
			"hooks":   []interface{}{hookEntry},
		},
	}
	for _, ht := range hookTypes {
		hooks[ht] = hookBlock
	}
	settings["hooks"] = hooks

	// 4. Write back settings.json
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(settingsPath, out, 0o644); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}

	// 5. Print instructions.
	fmt.Printf("Gru hooks installed for %s\n\n", projectDir)
	fmt.Printf("Hook script: %s\n", hookDst)
	fmt.Printf("Settings:    %s\n\n", settingsPath)
	fmt.Println("Next step: set GRU_API_KEY in your shell environment:")
	fmt.Println("  export GRU_API_KEY=<your-key>")
	fmt.Println("  # Optionally: export GRU_HOST=<host>  GRU_PORT=<port>")

	return nil
}

// copyFile copies src to dst with the given permission bits.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
