package main

import (
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newLaunchCmd(s *rootState) *cobra.Command {
	var profile string
	var name string
	var description string
	var envSpec string

	cmd := &cobra.Command{
		Use:   "launch <dir> <prompt>",
		Short: "Start a new agent session",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			msg := &gruv1.LaunchSessionRequest{
				ProjectDir:  args[0],
				Prompt:      args[1],
				Profile:     profile,
				Name:        name,
				Description: description,
			}
			if envSpec != "" {
				msg.EnvSpecPath = &envSpec
			}
			req := connect.NewRequest(msg)
			s.authReq(req)
			resp, err := s.client.LaunchSession(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("launch session: %w", err)
			}
			sess := resp.Msg.Session
			fmt.Fprintf(cmd.OutOrStdout(), "launched session %q %s (status: %s)\n",
				sess.Name, sess.Id[:8], statusLabel(sess.Status))
			return nil
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "", "agent profile name")
	cmd.Flags().StringVar(&name, "name", "", "human-readable session name (required)")
	cmd.Flags().StringVar(&description, "description", "", "what problem is being solved")
	cmd.Flags().StringVar(&envSpec, "env-spec", "",
		"optional path to an env-spec YAML (e.g. .gru/envs/minion-fullstack.yaml). "+
			"Relative paths resolve against <dir>. When unset, the server's default adapter is used.")
	return cmd
}
