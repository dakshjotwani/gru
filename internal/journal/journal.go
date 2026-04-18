// Package journal manages the Gru assistant singleton: a machine-scoped
// Claude Code session the operator talks to for spawning and triaging
// minions (other sessions). The server ensures exactly one live assistant
// session exists (identified by role="assistant") and respawns it when it
// dies. Package keeps its historic name for continuity with ~/.gru/journal/.
package journal

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dakshjotwani/gru/internal/config"
	"github.com/dakshjotwani/gru/internal/controller"
	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/google/uuid"
)

// Role is the constant stored in sessions.role that identifies the singleton.
const Role = "assistant"

// RuntimeID matches the ClaudeController runtime; the journal runs on claude-code.
const RuntimeID = "claude-code"

//go:embed prompt.md
var systemPrompt string

// Ensure guarantees a live journal session exists when cfg.Journal.IsEnabled()
// is true. It is safe to call on every server start: if a live journal session
// already exists, Ensure is a no-op. When cfg.Journal.IsEnabled() is false,
// Ensure does nothing.
func Ensure(ctx context.Context, s *store.Store, reg *controller.Registry, cfg *config.Config) error {
	if !cfg.Journal.IsEnabled() {
		return nil
	}
	existing, err := s.Queries().GetAssistantSession(ctx)
	if err == nil && existing.ID != "" {
		return nil
	}
	_, err = spawn(ctx, s, reg, cfg)
	return err
}

// Spawn always creates a new journal session, regardless of whether one exists.
// Callers (the supervisor) use this to restart the journal after a crash. The
// returned session ID is the newly created row.
func Spawn(ctx context.Context, s *store.Store, reg *controller.Registry, cfg *config.Config) (string, error) {
	return spawn(ctx, s, reg, cfg)
}

func spawn(ctx context.Context, s *store.Store, reg *controller.Registry, cfg *config.Config) (string, error) {
	if !cfg.Journal.IsEnabled() {
		return "", errors.New("journal is disabled")
	}

	dir, err := journalDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("journal: mkdir %s: %w", dir, err)
	}

	ctrl, err := reg.Get(RuntimeID)
	if err != nil {
		return "", fmt.Errorf("journal: %w", err)
	}

	projectID, err := upsertJournalProject(ctx, s, dir)
	if err != nil {
		return "", fmt.Errorf("journal: upsert project: %w", err)
	}

	sessionID := uuid.NewString()
	envRoots := strings.Join(cfg.Journal.WorkspaceRoots, ":")

	// Journal runs in host adapter against its own dir. worktree: false
	// (default) — the journal's directory isn't a git repo and --worktree
	// would be nonsensical.
	handle, err := ctrl.Launch(ctx, controller.LaunchOptions{
		SessionID:   sessionID,
		Profile:     "journal",
		ExtraPrompt: systemPrompt,
		Env: map[string]string{
			"GRU_JOURNAL_WORKSPACE_ROOTS": envRoots,
		},
		EnvSpec: env.EnvSpec{
			Name:     sessionID,
			Adapter:  "host",
			Workdirs: []string{dir},
		},
	})
	if err != nil {
		return "", fmt.Errorf("journal: launch: %w", err)
	}

	nilStr := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	profileName := "journal"

	_, err = s.Queries().CreateSession(ctx, store.CreateSessionParams{
		ID:          sessionID,
		ProjectID:   projectID,
		Runtime:     RuntimeID,
		Status:      "starting",
		Profile:     &profileName,
		TmuxSession: nilStr(handle.TmuxSession),
		TmuxWindow:  nilStr(handle.TmuxWindow),
		Name:        fmt.Sprintf("journal (%s)", time.Now().Format("2006-01-02")),
		Description: "Gru journal agent — always-on, machine-scoped",
		Prompt:      "",
		Role:        Role,
	})
	if err != nil {
		return "", fmt.Errorf("journal: create session row: %w", err)
	}

	log.Printf("journal: spawned session %s (tmux %s:%s)", sessionID[:8], handle.TmuxSession, handle.TmuxWindow)
	return sessionID, nil
}

func journalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("journal: resolve home: %w", err)
	}
	return filepath.Join(home, ".gru", "journal"), nil
}

// upsertJournalProject ensures a project row exists for the journal dir so the
// journal session has a project_id to reference. The project uses a stable
// "journal" id so every spawn reuses it.
func upsertJournalProject(ctx context.Context, s *store.Store, dir string) (string, error) {
	row, err := s.Queries().UpsertProject(ctx, store.UpsertProjectParams{
		ID:      "journal",
		Name:    "journal",
		Adapter: "host",
		Runtime: RuntimeID,
	})
	if err != nil {
		return "", err
	}
	return row.ID, nil
}
