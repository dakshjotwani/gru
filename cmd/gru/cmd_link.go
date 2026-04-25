package main

import (
	"fmt"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

// newLinkCmd is the parent for session-link subcommands. Today only `add`
// is wired; list/delete remain on the gRPC API for the dashboard.
func newLinkCmd(s *rootState) *cobra.Command {
	c := &cobra.Command{
		Use:   "link",
		Short: "Attach an external URL (PR, Slack thread, Figma) to the session",
	}
	c.AddCommand(newLinkAddCmd(s))
	return c
}

func newLinkAddCmd(s *rootState) *cobra.Command {
	var title, link string
	c := &cobra.Command{
		Use:   "add",
		Short: "Attach an external URL to the current session",
		Long: `Add a URL pointer to the current session.

Reads the session ID from <cwd>/.gru/session-id (same lookup the Claude
Code hook uses). The URL is validated server-side: scheme must be one of
https / http / mailto, and RFC1918 / link-local hostnames are rejected.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return fmt.Errorf("--title is required")
			}
			if link == "" {
				return fmt.Errorf("--url is required")
			}

			sessionID, err := readCWDSessionID()
			if err != nil {
				return err
			}

			req := connect.NewRequest(&gruv1.AddSessionLinkRequest{
				SessionId: sessionID,
				Title:     title,
				Url:       link,
			})
			resp, err := s.client.AddSessionLink(cmd.Context(), req)
			if err != nil {
				return fmt.Errorf("add link: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added link %q → %s\n", resp.Msg.Title, resp.Msg.Url)
			return nil
		},
	}
	c.Flags().StringVar(&title, "title", "", "chip label shown in the dashboard (required)")
	c.Flags().StringVar(&link, "url", "", "URL to attach (required)")
	return c
}
