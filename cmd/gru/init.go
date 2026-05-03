package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// hookScriptSrc is the path to the hook script to copy.
// Overridable in tests.
var hookScriptSrc = func() string {
	// Default: resolve relative to the binary's location using the module root convention.
	// At runtime the binary is built from the repo root; at test time repoRoot() is used.
	dir, _ := os.Executable()
	root := filepath.Dir(filepath.Dir(dir)) // binary lives in bin/, go up twice
	candidate := filepath.Join(root, "hooks", "claude-notify.sh")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	// Fallback: relative to working directory (useful when running directly with `go run`).
	return filepath.Join("hooks", "claude-notify.sh")
}()

// hookTypes is the rev-3 hook list. Every status-affecting hook
// Claude Code fires is registered against `gru hook ingest`, which
// translates the payload into gru's grammar and appends to the
// per-session event log. See docs/adr/0002-rev3-hook-driven-event-log.md.
//
// PreToolUse and SubagentStart/Stop are deliberately omitted — they
// don't drive status. PreCompact / PostCompact are also omitted (gru
// doesn't care about Claude's internal context management today;
// can be added when a use case appears, in which case translateEvent
// gains an arm and these strings get added here).
var hookTypes = []string{
	"SessionStart",
	"UserPromptSubmit",
	"Notification",
	"PostToolUse",
	"PostToolUseFailure",
	"Stop",
	"StopFailure",
}

// runInit implements the `gru init <project-dir>` subcommand.
func runInit(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gru init <project-dir>")
	}
	projectDir := args[0]

	// 1. Install hook script to ~/.gru/hooks/gru-hook.sh (global location).
	//    Claude Code runs hooks in a sanitized environment, so the script reads
	//    connection config from ~/.gru/server.yaml and the session ID from
	//    <project>/.gru/sessions/<shortID> (written at launch time).
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}
	// Single residual hook (rev 2): claude-notify.sh
	globalHookDst := filepath.Join(homeDir, ".gru", "hooks", "claude-notify.sh")
	if err := os.MkdirAll(filepath.Dir(globalHookDst), 0o755); err != nil {
		return fmt.Errorf("create ~/.gru/hooks dir: %w", err)
	}
	if err := copyFile(hookScriptSrc, globalHookDst, 0o755); err != nil {
		return fmt.Errorf("copy hook script: %w", err)
	}

	// 2. Install hooks into ~/.claude/settings.json (user-level).
	//    This fires for ALL Claude Code sessions, including worktree sessions
	//    that have their own .claude/ directory (which bypasses project-level settings).
	userSettingsDir := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(userSettingsDir, 0o755); err != nil {
		return fmt.Errorf("create ~/.claude dir: %w", err)
	}
	userSettingsPath := filepath.Join(userSettingsDir, "settings.json")
	if err := mergeHookSettings(userSettingsPath, globalHookDst); err != nil {
		return fmt.Errorf("update user settings: %w", err)
	}

	// 3. Also install into <project-dir>/.claude/settings.json for non-worktree sessions.
	projectSettingsDir := filepath.Join(projectDir, ".claude")
	if err := os.MkdirAll(projectSettingsDir, 0o755); err != nil {
		return fmt.Errorf("create project .claude dir: %w", err)
	}
	projectSettingsPath := filepath.Join(projectSettingsDir, "settings.json")
	if err := mergeHookSettings(projectSettingsPath, globalHookDst); err != nil {
		return fmt.Errorf("update project settings: %w", err)
	}

	fmt.Printf("Gru hooks installed\n\n")
	fmt.Printf("Hook script:      %s\n", globalHookDst)
	fmt.Printf("User settings:    %s\n", userSettingsPath)
	fmt.Printf("Project settings: %s\n\n", projectSettingsPath)
	fmt.Printf("Monitoring project: %s\n", projectDir)

	return nil
}

// mergeHookSettings reads (or creates) a settings.json at path and merges in the Gru hook entries.
func mergeHookSettings(settingsPath, hookScript string) error {
	settings := map[string]interface{}{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err2 := json.Unmarshal(data, &settings); err2 != nil {
			return fmt.Errorf("parse %s: %w", settingsPath, err2)
		}
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = map[string]interface{}{}
	}

	// Self-heal: strip legacy rev-1 entries that point at gru-hook.sh.
	// `gru init` was previously installed against many more hook events
	// (PostToolUse, PreToolUse, SessionStart, Stop, ...). Rev 2 only
	// uses Notification; the leftover entries fire from sibling Claude
	// processes and either spawn no-op hooks (cosmetic noise) or, when
	// gru-hook.sh still writes to ~/.gru/notify/, re-introduce the
	// transcript-path hijack the tailer now defends against. Removing
	// them here makes a re-run of `gru init` the canonical cleanup.
	for evt, block := range hooks {
		if hookHasGruScript(block) && !isRev2HookType(evt) {
			delete(hooks, evt)
		}
	}

	hookEntry := map[string]interface{}{
		"type":    "command",
		"command": hookScript,
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

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(settingsPath, out, 0o644)
}

// isRev2HookType reports whether the given hook event is part of the
// current rev-2 install set. Anything else previously written by `gru
// init` is a stale rev-1 entry to be removed.
func isRev2HookType(evt string) bool {
	for _, ht := range hookTypes {
		if ht == evt {
			return true
		}
	}
	return false
}

// hookHasGruScript reports whether any command inside this hook block
// points at a script under ~/.gru/hooks/. We only strip entries Gru
// installed itself; never touch a third-party hook the user added.
func hookHasGruScript(block interface{}) bool {
	arr, ok := block.([]interface{})
	if !ok {
		return false
	}
	for _, item := range arr {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		hs, _ := m["hooks"].([]interface{})
		for _, h := range hs {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			cmd, _ := hm["command"].(string)
			if strings.Contains(cmd, "/.gru/hooks/") {
				return true
			}
		}
	}
	return false
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

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init <project-dir>",
		Short: "Install Claude Code hook scripts into a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(args)
		},
	}
}
