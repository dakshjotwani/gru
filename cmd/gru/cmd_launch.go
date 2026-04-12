package main

import (
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newLaunchCmd(s *rootState) *cobra.Command {
	var profile string

	cmd := &cobra.Command{
		Use:   "launch <dir> <prompt>",
		Short: "Start a new agent session",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := connect.NewRequest(&gruv1.LaunchSessionRequest{
				ProjectDir: args[0],
				Prompt:     args[1],
				Profile:    profile,
			})
			s.authReq(req)
			resp, err := s.client.LaunchSession(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("launch session: %w", err)
			}
			sess := resp.Msg.Session
			fmt.Fprintf(cmd.OutOrStdout(), "launched session %s (status: %s)\n",
				sess.Id, statusLabel(sess.Status))
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "", "agent profile name")
	return cmd
}
