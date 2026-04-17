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
			s.authReq(req)
			resp, err := s.client.ListProjects(ctx, req)
			if err != nil {
				return fmt.Errorf("list projects: %w", err)
			}
			projects := resp.Msg.Projects

			if jsonOut {
				// Hand-render to keep the output stable and small — proto-json
				// would include unset fields and protobuf-specific noise that
				// callers don't need.
				type projectOut struct {
					ID                 string   `json:"id"`
					Name               string   `json:"name"`
					Path               string   `json:"path"`
					Runtime            string   `json:"runtime"`
					AdditionalWorkdirs []string `json:"additional_workdirs,omitempty"`
				}
				list := make([]projectOut, 0, len(projects))
				for _, p := range projects {
					list = append(list, projectOut{
						ID:                 p.Id,
						Name:               p.Name,
						Path:               p.Path,
						Runtime:            p.Runtime,
						AdditionalWorkdirs: p.AdditionalWorkdirs,
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
			fmt.Fprintf(out, "%-36s  %-20s  %s\n", "ID", "NAME", "PATH")
			fmt.Fprintln(out, hrule(80))
			for _, p := range projects {
				fmt.Fprintf(out, "%-36s  %-20s  %s\n", p.Id, p.Name, p.Path)
			}
			return nil
		},
	}
	listCmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a human-readable table")
	projectsCmd.AddCommand(listCmd)
	return projectsCmd
}
