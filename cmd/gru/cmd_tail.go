package main

import (
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newTailCmd(s *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "tail <session-id>",
		Short: "Stream live events for a session (Ctrl+C to stop)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			req := connect.NewRequest(&gruv1.SubscribeEventsRequest{
				ProjectIds: []string{args[0]},
			})
			s.authReq(req)

			stream, err := s.client.SubscribeEvents(ctx, req)
			if err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
			defer stream.Close()

			fmt.Fprintf(out, "tailing events for %s (Ctrl+C to stop)...\n", shortID(args[0]))
			for stream.Receive() {
				ev := stream.Msg()
				ts := "-"
				if ev.Timestamp != nil {
					ts = ev.Timestamp.AsTime().Format("15:04:05")
				}
				fmt.Fprintf(out, "[%s] %-30s  session=%s\n", ts, ev.Type, shortID(ev.SessionId))
			}
			if err := stream.Err(); err != nil && ctx.Err() == nil {
				return fmt.Errorf("stream error: %w", err)
			}
			return nil
		},
	}
}
