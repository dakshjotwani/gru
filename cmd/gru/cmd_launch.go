package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	gruv1 "github.com/dakshjotwani/gru/proto/gru/v1"
	"github.com/spf13/cobra"
)

func newLaunchCmd(s *rootState) *cobra.Command {
	var profile string
	var name string
	var description string

	cmd := &cobra.Command{
		Use:   "launch <env-or-dir> <prompt>",
		Short: "Start a new agent session",
		Long: `Start a new agent session against an env spec.

The first argument can be:

  - An existing project name (resolves to ~/.gru/projects/<name>/spec.yaml)
  - A path to a spec.yaml file (ad-hoc specs outside the canonical location)
  - A filesystem directory — the "just here" shortcut; the CLI creates a
    host-adapter spec at ~/.gru/projects/<basename>/spec.yaml pointing at
    the given directory, then launches against it.

See docs/superpowers/specs/2026-04-17-env-centric-launch-design.md for the
full addressing model.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			envSpec, err := resolveOrSynthesizeSpec(args[0])
			if err != nil {
				return err
			}
			msg := &gruv1.LaunchSessionRequest{
				EnvSpec:     envSpec,
				Prompt:      args[1],
				Profile:     profile,
				Name:        name,
				Description: description,
			}
			req := connect.NewRequest(msg)
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
	return cmd
}

// resolveOrSynthesizeSpec interprets the first positional argument per the
// CLI addressing rules:
//
//   - ends in .yaml/.yml → treat as a spec file path (relative or absolute)
//   - exists as a directory → synthesize a host-adapter spec at
//     ~/.gru/projects/<basename>/spec.yaml pointing at that dir
//   - otherwise → treat as a project name (server resolves to
//     ~/.gru/projects/<name>/spec.yaml)
//
// Returns the string to put in LaunchSessionRequest.EnvSpec.
func resolveOrSynthesizeSpec(arg string) (string, error) {
	if strings.HasSuffix(arg, ".yaml") || strings.HasSuffix(arg, ".yml") {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return "", fmt.Errorf("resolve spec path %s: %w", arg, err)
		}
		return abs, nil
	}
	// If the arg exists as a directory, synth a host spec.
	if info, err := os.Stat(arg); err == nil && info.IsDir() {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return "", fmt.Errorf("resolve dir %s: %w", arg, err)
		}
		return synthesizeHostSpec(abs)
	}
	// Otherwise treat it as a project name and let the server resolve.
	return arg, nil
}

// synthesizeHostSpec writes (or reuses) a minimal host-adapter spec under
// ~/.gru/projects/<basename-of-dir>/spec.yaml pointing at the given
// directory. Returns the absolute path to that spec.yaml. Idempotent:
// running `gru launch ~/foo ...` twice points at the same spec.
//
// On basename collision (two different paths with the same basename), we
// append a numeric tiebreaker: foo, foo-2, foo-3, etc. Each tiebreaker
// slot records the source dir in its spec; we only claim a tiebreaker if
// the existing spec points at a DIFFERENT dir.
func synthesizeHostSpec(dir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	projectsRoot := filepath.Join(home, ".gru", "projects")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		return "", fmt.Errorf("create projects root: %w", err)
	}

	base := filepath.Base(dir)
	if base == "" || base == "/" {
		base = "project"
	}

	for i := 0; i < 100; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i+1)
		}
		projectDir := filepath.Join(projectsRoot, name)
		specPath := filepath.Join(projectDir, "spec.yaml")
		existing, _ := os.ReadFile(specPath)
		if existing != nil {
			// Reuse only if the stored workdir matches. Otherwise move to
			// the next tiebreaker slot.
			if strings.Contains(string(existing), "\n  - "+dir+"\n") {
				return specPath, nil
			}
			continue
		}
		if err := os.MkdirAll(projectDir, 0o755); err != nil {
			return "", fmt.Errorf("create project dir %s: %w", projectDir, err)
		}
		body := fmt.Sprintf(`name: %s
adapter: host
workdirs:
  - %s
`, name, dir)
		if err := os.WriteFile(specPath, []byte(body), 0o644); err != nil {
			return "", fmt.Errorf("write spec %s: %w", specPath, err)
		}
		return specPath, nil
	}
	return "", fmt.Errorf("exhausted 100 tiebreaker slots for basename %q", base)
}
