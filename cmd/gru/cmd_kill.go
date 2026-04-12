package main

import (
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newKillCmd(s *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "kill <id>",
		Short: "Terminate a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := connect.NewRequest(&gruv1.KillSessionRequest{Id: args[0]})
			s.authReq(req)
			resp, err := s.client.KillSession(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("kill session: %w", err)
			}
			if resp.Msg.Success {
				fmt.Fprintf(cmd.OutOrStdout(), "session %s killed successfully\n", shortID(args[0]))
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "kill did not succeed for session %s\n", shortID(args[0]))
			}
			return nil
		},
	}
}
