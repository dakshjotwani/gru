package command_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/env/command"
)

// TestEventsScriptRespawn kills the events script and verifies that the
// adapter synthesizes an error event, respawns the script, and begins
// receiving fresh events. Covers conformance case #8.
func TestEventsScriptRespawn(t *testing.T) {
	origBackoff := command.RespawnBackoff
	origHeartbeat := command.HeartbeatTimeout
	command.RespawnBackoff = 100 * time.Millisecond
	command.HeartbeatTimeout = 1 * time.Second
	defer func() {
		command.RespawnBackoff = origBackoff
		command.HeartbeatTimeout = origHeartbeat
	}()

	scriptDir := t.TempDir()
	workdir := t.TempDir()
	sentinel := filepath.Join(workdir, "events.log")

	// This events script emits a "started" event then exits immediately.
	// That lets us observe the respawn behavior: after the first exit, the
	// adapter emits an error event, waits RespawnBackoff, then starts the
	// script again. We count restarts via the sentinel file.
	eventsScript := `#!/usr/bin/env bash
set -euo pipefail
echo ran >> ` + sentinel + `
printf '{"kind":"started","detail":"attempt '"$(wc -l < ` + sentinel + `)"'"}\n'
exit 0
`
	createScript := `#!/usr/bin/env bash
printf '{"provider_ref":"x","pty_holders":["tmux"]}\n'
`
	destroyScript := `#!/usr/bin/env bash
exit 0
`
	writeExec := func(name, body string) string {
		p := filepath.Join(scriptDir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	createPath := writeExec("create.sh", createScript)
	destroyPath := writeExec("destroy.sh", destroyScript)
	eventsPath := writeExec("events.sh", eventsScript)

	a := command.New()
	spec := env.EnvSpec{
		Name:    "respawn-test",
		Adapter: "command",
		Config: map[string]any{
			"create":   "bash " + createPath,
			"exec":     "bash -c", // unused in this test
			"exec_pty": "bash -c", // unused
			"destroy":  "bash " + destroyPath,
			"events":   "bash " + eventsPath,
		},
		Workdirs: []string{workdir},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	inst, err := a.Create(ctx, spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer a.Destroy(ctx, inst)

	evCh, err := a.Events(ctx, inst)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	// Collect events for a few seconds; we expect at least one "started"
	// and at least one error ("events script exited ...") interleaved.
	var kinds []string
	deadline := time.After(3 * time.Second)
collect:
	for {
		select {
		case evt, ok := <-evCh:
			if !ok {
				break collect
			}
			kinds = append(kinds, evt.Kind+":"+evt.Detail)
			if strings.Contains(evt.Detail, "attempt") && countContains(kinds, "started:") >= 2 {
				// Saw at least two distinct starts → respawn confirmed.
				break collect
			}
		case <-deadline:
			break collect
		}
	}

	if countContains(kinds, "started:") < 2 {
		t.Fatalf("expected at least 2 started events (respawn), got %d: %v", countContains(kinds, "started:"), kinds)
	}
	if !containsAny(kinds, "events script exited") {
		t.Fatalf("expected an error event after script exit, got: %v", kinds)
	}
}

func countContains(xs []string, needle string) int {
	n := 0
	for _, s := range xs {
		if strings.Contains(s, needle) {
			n++
		}
	}
	return n
}

func containsAny(xs []string, needle string) bool {
	for _, s := range xs {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// TestCreateScriptErrorTable exercises each row of the create-script outcome
// table in the spec: exit code × stdout validity × provider_ref presence.
func TestCreateScriptErrorTable(t *testing.T) {
	type tc struct {
		name        string
		script      string
		wantErrText string
	}
	cases := []tc{
		{
			name:        "exit0_empty_stdout",
			script:      `exit 0`,
			wantErrText: "no stdout JSON",
		},
		{
			name:        "exit0_invalid_json",
			script:      `echo "not json at all"; exit 0`,
			wantErrText: "not valid JSON",
		},
		{
			name:        "exit0_missing_provider_ref",
			script:      `echo '{"pty_holders":["tmux"]}'; exit 0`,
			wantErrText: "missing provider_ref",
		},
		{
			name:        "exit_nonzero_no_json",
			script:      `echo "boom" >&2; exit 3`,
			wantErrText: "exited 3",
		},
		{
			name:        "exit_nonzero_with_json",
			script:      `echo '{"provider_ref":"leftover"}'; exit 7`,
			wantErrText: "exited 7",
		},
	}

	scriptDir := t.TempDir()
	destroyPath := filepath.Join(scriptDir, "destroy.sh")
	_ = os.WriteFile(destroyPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755)

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			createPath := filepath.Join(scriptDir, fmt.Sprintf("create-%d.sh", i))
			body := "#!/usr/bin/env bash\n" + c.script + "\n"
			if err := os.WriteFile(createPath, []byte(body), 0o755); err != nil {
				t.Fatalf("write: %v", err)
			}

			a := command.New()
			spec := env.EnvSpec{
				Name:    "tbl-" + c.name,
				Adapter: "command",
				Config: map[string]any{
					"create":   "bash " + createPath,
					"exec":     "bash -c",
					"exec_pty": "bash -c",
					"destroy":  "bash " + destroyPath,
				},
				Workdirs: []string{t.TempDir()},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := a.Create(ctx, spec)
			if err == nil {
				t.Fatalf("Create succeeded; expected failure with %q", c.wantErrText)
			}
			if !strings.Contains(err.Error(), c.wantErrText) {
				t.Fatalf("err %q does not contain %q", err.Error(), c.wantErrText)
			}
		})
	}
}

// TestCreateScriptTimeout verifies that a create script hanging past the
// CreateTimeout is killed and the Create() call fails with a timeout error.
func TestCreateScriptTimeout(t *testing.T) {
	orig := command.CreateTimeout
	command.CreateTimeout = 500 * time.Millisecond
	defer func() { command.CreateTimeout = orig }()

	scriptDir := t.TempDir()
	createPath := filepath.Join(scriptDir, "create.sh")
	destroyPath := filepath.Join(scriptDir, "destroy.sh")
	_ = os.WriteFile(createPath, []byte("#!/usr/bin/env bash\nsleep 5\n"), 0o755)
	_ = os.WriteFile(destroyPath, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755)

	a := command.New()
	spec := env.EnvSpec{
		Name:    "timeout-test",
		Adapter: "command",
		Config: map[string]any{
			"create":   "bash " + createPath,
			"exec":     "bash -c",
			"exec_pty": "bash -c",
			"destroy":  "bash " + destroyPath,
		},
		Workdirs: []string{t.TempDir()},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := a.Create(ctx, spec)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
