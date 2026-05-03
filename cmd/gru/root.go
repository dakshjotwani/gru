package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/dakshjotwani/gru/internal/config"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/dakshjotwani/gru/proto/gru/v1/gruv1connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type rootState struct {
	serverURL string
	client    gruv1connect.GruServiceClient
}

func newRootCmd() *cobra.Command {
	state := &rootState{}

	root := &cobra.Command{
		Use:          "gru",
		Short:        "Mission control for AI agent fleets",
		Long:         "Gru monitors, launches, and manages AI coding agent sessions across projects.",
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "server" || cmd.Name() == "prune" {
				return nil
			}
			// `gru env` subcommands don't talk to the server — skip config
			// load so they work before the operator has ever run `gru init`.
			if parent := cmd.Parent(); parent != nil && parent.Name() == "env" {
				return nil
			}
			if state.serverURL == "" {
				cfg, err := config.Load(defaultConfigPath())
				if err != nil {
					return fmt.Errorf("load config: %w", err)
				}
				// cfg.Addr is typically ":7777" — the CLI reaches the
				// server over loopback regardless of how the server is
				// bound externally (tailnet/all).
				addr := cfg.Addr
				if strings.HasPrefix(addr, ":") {
					addr = "127.0.0.1" + addr
				}
				state.serverURL = "http://" + addr
			}
			state.client = gruv1connect.NewGruServiceClient(
				&http.Client{Timeout: 30 * time.Second},
				state.serverURL,
			)
			return nil
		},
	}

	root.PersistentFlags().StringVar(&state.serverURL, "server", "", "gru server URL (default: from ~/.gru/server.yaml)")

	root.AddCommand(
		newServerCmd(),
		newInitCmd(),
		newPruneCmd(),
		newStatusCmd(state),
		newKillCmd(state),
		newLaunchCmd(state),
		newTailCmd(state),
		newAttachCmd(state),
		newEnvCmd(),
		newProjectsCmd(state),
		newArtifactCmd(state),
		newLinkCmd(state),
		newHookCmd(),
	)

	return root
}

func defaultConfigPath() string {
	return filepath.Join(stateDir(), "server.yaml")
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func statusLabel(s gruv1.SessionStatus) string {
	switch s {
	case gruv1.SessionStatus_SESSION_STATUS_STARTING:
		return "starting"
	case gruv1.SessionStatus_SESSION_STATUS_RUNNING:
		return "running"
	case gruv1.SessionStatus_SESSION_STATUS_IDLE:
		return "idle"
	case gruv1.SessionStatus_SESSION_STATUS_NEEDS_ATTENTION:
		return "needs_attention"
	case gruv1.SessionStatus_SESSION_STATUS_COMPLETED:
		return "completed"
	case gruv1.SessionStatus_SESSION_STATUS_ERRORED:
		return "errored"
	case gruv1.SessionStatus_SESSION_STATUS_KILLED:
		return "killed"
	default:
		return "unknown"
	}
}

func formatUptime(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return "-"
	}
	d := time.Since(ts.AsTime()).Round(time.Second)
	if d < 0 {
		return "0s"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, sec)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

func hrule(w int) string { return strings.Repeat("-", w) }
