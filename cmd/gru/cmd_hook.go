package main

import (
	"fmt"
	"io"
	"os"

	"github.com/dakshjotwani/gru/internal/ingest"
	"github.com/spf13/cobra"
)

// newHookCmd builds the `gru hook` subcommand group. Today the only
// child is `ingest` — invoked by Claude Code hooks (via a one-line
// shell wrapper that calls `exec gru hook ingest`) and by gru's own
// supervisor / CLI handlers when they run out-of-process.
//
// In-process callers (the supervisor goroutine, gRPC handlers in this
// same gru server) should use ingest.Append directly rather than
// shelling out — this command is the on-disk entry point only.
func newHookCmd() *cobra.Command {
	hookCmd := &cobra.Command{
		Use:   "hook",
		Short: "Hook entry points (called by Claude Code, not by humans)",
	}
	hookCmd.AddCommand(newHookIngestCmd())
	return hookCmd
}

func newHookIngestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ingest",
		Short: "Translate a Claude Code hook payload (stdin) into a gru event and append to the per-session log",
		// Always exit 0 even on rejection: a non-zero exit code in a
		// Claude hook can block the agent's tool call, which would be
		// a much worse failure mode than silently dropping a hook.
		// Real errors (translator parse failure) print to stderr.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHookIngest(cmd.InOrStdin(), cmd.ErrOrStderr())
		},
	}
}

func runHookIngest(stdin io.Reader, stderr io.Writer) error {
	payload, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "gru hook ingest: read stdin: %v\n", err)
		return nil
	}
	if len(payload) == 0 {
		// No-op (Claude invoked us with no JSON — probably misconfig).
		return nil
	}
	sessionID, ev, err := ingest.TranslateClaudeHook(payload)
	if err != nil {
		fmt.Fprintf(stderr, "gru hook ingest: translate: %v\n", err)
		return nil
	}
	if sessionID == "" {
		// Either not a gru-launched session, or sibling-Claude guard
		// rejected the payload. Silent skip — the rejection is by
		// design, not an error.
		return nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "gru hook ingest: home dir: %v\n", err)
		return nil
	}
	if err := ingest.Append(homeDir, sessionID, ev); err != nil {
		fmt.Fprintf(stderr, "gru hook ingest: append: %v\n", err)
		return nil
	}
	return nil
}
