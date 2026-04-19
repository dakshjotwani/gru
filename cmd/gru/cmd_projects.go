package main

import (
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

// newProjectsCmd exposes `gru projects list` for scripts and the Gru
// assistant. Emits a short human-readable table by default and strict JSON
// behind --json so the assistant can parse it without regex heuristics.
func newProjectsCmd(s *rootState) *cobra.Command {
	projectsCmd := &cobra.Command{
		Use:   "projects",
		Short: "Inspect registered projects",
	}

	var jsonOut bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all registered projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			req := connect.NewRequest(&gruv1.ListProjectsRequest{})
			resp, err := s.client.ListProjects(ctx, req)
			if err != nil {
				return fmt.Errorf("list projects: %w", err)
			}
			projects := resp.Msg.Projects

			if jsonOut {
				// Hand-render to keep the output stable and small.
				type projectOut struct {
					ID      string `json:"id"`       // absolute spec path
					Name    string `json:"name"`
					Adapter string `json:"adapter"`
					Runtime string `json:"runtime"`
				}
				list := make([]projectOut, 0, len(projects))
				for _, p := range projects {
					list = append(list, projectOut{
						ID:      p.Id,
						Name:    p.Name,
						Adapter: p.Adapter,
						Runtime: p.Runtime,
					})
				}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(list)
			}

			if len(projects) == 0 {
				fmt.Fprintln(out, "no projects")
				return nil
			}
			fmt.Fprintf(out, "%-20s  %-10s  %s\n", "NAME", "ADAPTER", "SPEC")
			fmt.Fprintln(out, hrule(80))
			for _, p := range projects {
				fmt.Fprintf(out, "%-20s  %-10s  %s\n", p.Name, p.Adapter, p.Id)
			}
			return nil
		},
	}
	listCmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a human-readable table")
	projectsCmd.AddCommand(listCmd)
	return projectsCmd
}
