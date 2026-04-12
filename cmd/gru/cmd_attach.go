package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newAttachCmd(s *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "attach <session-id-or-project-name>",
		Short: "Attach to a running session in tmux",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			req := connect.NewRequest(&gruv1.GetSessionRequest{Id: args[0]})
			s.authReq(req)
			resp, err := s.client.GetSession(ctx, req)
			if err != nil {
				tmuxSession := "gru-" + sanitizeProjectName(args[0])
				return execTmuxAttach(tmuxSession, "")
			}
			sess := resp.Msg
			if sess.TmuxSession == "" {
				return fmt.Errorf("session %s has no tmux session (not launched by gru)", shortID(args[0]))
			}
			return execTmuxAttach(sess.TmuxSession, sess.TmuxWindow)
		},
	}
}

func sanitizeProjectName(name string) string {
	result := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			result = append(result, c+32)
		case c == '/' || c == ' ' || c == '.':
			result = append(result, '-')
		default:
			result = append(result, c)
		}
	}
	s := string(result)
	if len(s) > 4 && s[:4] == "gru-" {
		s = s[4:]
	}
	return s
}

func execTmuxAttach(session, window string) error {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found in PATH: %w", err)
	}
	args := []string{"tmux", "attach-session", "-t", session}
	if window != "" {
		args = append(args, ";", "select-window", "-t", window)
	}
	return syscall.Exec(tmuxPath, args, os.Environ())
}
