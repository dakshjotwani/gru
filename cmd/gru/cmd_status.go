package main

import (
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newStatusCmd(s *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "status [id]",
		Short: "List all sessions, or show detail for one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			if len(args) == 1 {
				req := connect.NewRequest(&gruv1.GetSessionRequest{Id: args[0]})
				resp, err := s.client.GetSession(ctx, req)
				if err != nil {
					return fmt.Errorf("get session: %w", err)
				}
				sess := resp.Msg
				fmt.Fprintf(out, "ID:       %s\n", sess.Id)
				fmt.Fprintf(out, "Project:  %s\n", sess.ProjectId)
				fmt.Fprintf(out, "Runtime:  %s\n", sess.Runtime)
				fmt.Fprintf(out, "Status:   %s\n", statusLabel(sess.Status))
				fmt.Fprintf(out, "Attn:     %.2f\n", sess.AttentionScore)
				fmt.Fprintf(out, "PID:      %d\n", sess.Pid)
				if sess.StartedAt != nil {
					fmt.Fprintf(out, "Started:  %s\n", sess.StartedAt.AsTime().Format("2006-01-02 15:04:05"))
					fmt.Fprintf(out, "Uptime:   %s\n", formatUptime(sess.StartedAt))
				}
				if sess.Profile != "" {
					fmt.Fprintf(out, "Profile:  %s\n", sess.Profile)
				}
				return nil
			}

			req := connect.NewRequest(&gruv1.ListSessionsRequest{})
			resp, err := s.client.ListSessions(ctx, req)
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			sessions := resp.Msg.Sessions
			if len(sessions) == 0 {
				fmt.Fprintln(out, "no sessions")
				return nil
			}
			fmt.Fprintf(out, "%-12s  %-20s  %-18s  %-6s  %s\n", "ID", "PROJECT", "STATUS", "ATTN", "UPTIME")
			fmt.Fprintln(out, hrule(72))
			for _, sess := range sessions {
				fmt.Fprintf(out, "%-12s  %-20s  %-18s  %-6.2f  %s\n",
					shortID(sess.Id), sess.ProjectId,
					statusLabel(sess.Status), sess.AttentionScore,
					formatUptime(sess.StartedAt))
			}
			return nil
		},
	}
}
