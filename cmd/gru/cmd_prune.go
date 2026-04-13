package main

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/dakshjotwani/gru/internal/config"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/spf13/cobra"
)

func newPruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Delete completed, errored, and killed sessions from the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(defaultConfigPath())
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			s, err := store.Open(cfg.DBPath)
			if err != nil {
				return fmt.Errorf("open store: %w", err)
			}
			defer s.Close()

			ctx := context.Background()
			rows, err := s.Queries().ListSessions(ctx, store.ListSessionsParams{})
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}

			pruned := 0
			for _, row := range rows {
				switch row.Status {
				case "errored", "completed", "killed":
					// best-effort: kill the tmux window in case it's still open
					if row.TmuxSession != nil && row.TmuxWindow != nil {
						target := *row.TmuxSession + ":" + *row.TmuxWindow
						_ = exec.Command("tmux", "kill-window", "-t", target).Run()
					}
					// delete events first (FK constraint), then the session
					if _, err := s.DB().ExecContext(ctx,
						`DELETE FROM events WHERE session_id = ?`, row.ID,
					); err != nil {
						return fmt.Errorf("delete events for %s: %w", row.ID, err)
					}
					if _, err := s.DB().ExecContext(ctx,
						`DELETE FROM sessions WHERE id = ?`, row.ID,
					); err != nil {
						return fmt.Errorf("delete session %s: %w", row.ID, err)
					}
					pruned++
				}
			}

			fmt.Printf("pruned %d session(s)\n", pruned)
			return nil
		},
	}
}
