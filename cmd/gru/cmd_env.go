package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/env/command"
	"github.com/dakshjotwani/gru/internal/env/conformance"
	"github.com/dakshjotwani/gru/internal/env/host"
)

// specFile is the on-disk representation of an EnvSpec.
type specFile struct {
	Name     string         `yaml:"name"`
	Adapter  string         `yaml:"adapter"`
	Workdirs []string       `yaml:"workdirs"`
	Config   map[string]any `yaml:"config"`
}

func loadSpecFile(path string) (env.EnvSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return env.EnvSpec{}, fmt.Errorf("read spec file: %w", err)
	}
	var sf specFile
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return env.EnvSpec{}, fmt.Errorf("parse spec file %s: %w", path, err)
	}
	if sf.Adapter == "" {
		return env.EnvSpec{}, fmt.Errorf("spec file %s is missing 'adapter'", path)
	}
	if len(sf.Workdirs) == 0 {
		return env.EnvSpec{}, fmt.Errorf("spec file %s is missing 'workdirs' (need at least one)", path)
	}
	for i, wd := range sf.Workdirs {
		if !filepath.IsAbs(wd) {
			sf.Workdirs[i] = filepath.Join(filepath.Dir(path), wd)
		}
	}
	name := sf.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return env.EnvSpec{
		Name:     name,
		Adapter:  sf.Adapter,
		Workdirs: sf.Workdirs,
		Config:   sf.Config,
	}, nil
}

// buildEnvRegistry returns a Registry populated with the adapters Gru ships.
func buildEnvRegistry() *env.Registry {
	r := env.NewRegistry()
	r.Register(host.New())
	r.Register(command.New())
	return r
}

func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Inspect and test environment specs",
		Long: `Gru's "env" subcommands work with environment specs — the contract
between Gru and the infrastructure a session runs inside.

See docs/superpowers/specs/2026-04-17-gru-v2-design.md for the full contract.`,
	}
	cmd.AddCommand(newEnvListCmd())
	cmd.AddCommand(newEnvShowCmd())
	cmd.AddCommand(newEnvTestCmd())
	return cmd
}

func newEnvListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered environment adapters",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg := buildEnvRegistry()
			for _, id := range reg.List() {
				fmt.Println(id)
			}
			return nil
		},
	}
}

func newEnvShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <adapter>",
		Short: "Print the config schema an adapter expects",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "host":
				fmt.Print(`# host adapter — runs directly on the operator's machine.
# Config is empty. Workdirs are used as-is.
# Isolation: none. One session per workdir set (enforced).
adapter: host
workdirs:
  - /path/to/your/project
`)
			case "command":
				fmt.Print(`# command adapter — wraps user-supplied shell scripts.
# Every config.* value is a text/template with {{.SessionID}}, {{.Workdir}},
# {{.Workdirs}}, {{.ProviderRef}}, {{.EnvSpecConfig}} available.
adapter: command
workdirs:
  - /path/to/your/project
config:
  create:   "scripts/gru-env/create.sh {{.SessionID}} {{.Workdir}}"
  exec:     "scripts/gru-env/exec.sh {{.ProviderRef}}"
  exec_pty: "scripts/gru-env/exec-pty.sh {{.ProviderRef}}"
  destroy:  "scripts/gru-env/destroy.sh {{.ProviderRef}}"
  events:   "scripts/gru-env/events.sh {{.ProviderRef}}"   # optional
  status:   "scripts/gru-env/status.sh {{.ProviderRef}}"   # optional
`)
			default:
				return fmt.Errorf("unknown adapter %q (try 'host' or 'command')", args[0])
			}
			return nil
		},
	}
}

func newEnvTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <spec.yaml>",
		Short: "Run the Environment conformance suite against a user spec",
		Long: `Runs the 9-case conformance suite documented in the v2 design spec
(§Success criteria #6) against the adapter named in <spec.yaml>.

Exit code 0 on pass; non-zero with a summary on fail.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := loadSpecFile(args[0])
			if err != nil {
				return err
			}
			reg := buildEnvRegistry()
			adapter, err := reg.Get(spec.Adapter)
			if err != nil {
				return err
			}
			counter := 0
			mkSpec := func(r conformance.Reporter) env.EnvSpec {
				counter++
				out := spec
				out.Name = fmt.Sprintf("%s-%d-%s", spec.Name, counter, time.Now().Format("150405"))
				return out
			}

			suite := conformance.Suite{
				Name:    spec.Adapter,
				Adapter: adapter,
				NewSpec: mkSpec,
				// No KillBackingResource for the generic runner — it would
				// need adapter-specific knowledge to simulate loss.
				SupportsEventsRespawn: spec.Adapter == "command",
			}

			fmt.Printf("conformance run for adapter=%s spec=%s\n", spec.Adapter, spec.Name)
			var passed, failed, skipped int
			for _, c := range conformance.Cases() {
				rep := conformance.RunOne(c.Fn, suite)
				status := "PASS"
				switch {
				case rep.Failed():
					status = "FAIL"
					failed++
				case rep.Skipped():
					status = "SKIP"
					skipped++
				default:
					passed++
				}
				fmt.Printf("  %-24s %s\n", c.Name, status)
				if logs := strings.TrimSpace(rep.Logs.String()); logs != "" {
					for _, line := range strings.Split(logs, "\n") {
						fmt.Printf("    %s\n", line)
					}
				}
			}
			fmt.Printf("\n%d pass, %d fail, %d skip\n", passed, failed, skipped)
			if failed > 0 {
				os.Exit(1)
			}
			return nil
		},
	}
}
