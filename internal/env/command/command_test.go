package command_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/env/command"
	"github.com/dakshjotwani/gru/internal/env/conformance"
)

func TestCommandAdapter_Conformance(t *testing.T) {
	fixtures := fixtureDir(t)
	sessionCounter := 0
	adapter := command.New()

	newSpec := func(r conformance.Reporter) env.EnvSpec {
		sessionCounter++
		// t.TempDir captured from the outer function to auto-clean.
		workdir, err := os.MkdirTemp("", "cmd-spec-*")
		if err != nil {
			r.Fatalf("mkdir tmp: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(workdir) })
		sessionID := fmt.Sprintf("cmd-%d-%s", sessionCounter, time.Now().Format("150405"))
		return env.EnvSpec{
			Name:    sessionID,
			Adapter: "command",
			Config: map[string]any{
				"create":   fmt.Sprintf("bash %s/create.sh %s %s", fixtures, sessionID, workdir),
				"exec":     fmt.Sprintf("bash %s/exec.sh {{.ProviderRef}}", fixtures),
				"exec_pty": fmt.Sprintf("bash %s/exec-pty.sh {{.ProviderRef}}", fixtures),
				"destroy":  fmt.Sprintf("bash %s/destroy.sh {{.ProviderRef}}", fixtures),
				"events":   fmt.Sprintf("bash %s/events.sh {{.ProviderRef}}", fixtures),
				"status":   fmt.Sprintf("bash %s/status.sh {{.ProviderRef}}", fixtures),
			},
			Workdirs: []string{workdir},
		}
	}

	conformance.Run(t, conformance.Suite{
		Name:    "command",
		Adapter: adapter,
		NewSpec: newSpec,
		KillBackingResource: func(r conformance.Reporter, inst env.Instance) {
			ref, err := unwrapUserRef(inst.ProviderRef)
			if err != nil {
				r.Fatalf("decode provider ref: %v", err)
				return
			}
			if err := os.RemoveAll(ref); err != nil {
				r.Fatalf("remove sandbox: %v", err)
			}
		},
		// events.sh emits a "started" event on launch; caseEventsPump can
		// rely on that without a force hook.
		ForceLifecycleEvent:   nil,
		SupportsEventsRespawn: true,
	})
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	// Walk up from this file location until we find the repo root (go.mod).
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("cannot locate caller")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "test", "fixtures", "command-adapter")
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not find repo root from %s", file)
	return ""
}

// unwrapUserRef decodes the adapter's internal wrapper around the user-script
// provider_ref. Conformance tests treat ProviderRef as opaque from the
// public interface's perspective, but adapter-specific tests need to reach in.
func unwrapUserRef(providerRef string) (string, error) {
	// The wrapper format is defined in internal/env/command. Use a minimal
	// shadow struct to avoid exporting internals just for tests.
	var raw struct {
		UserRef string `json:"user_ref"`
	}
	return raw.UserRef, decodeJSON([]byte(providerRef), &raw)
}

// decodeJSON is a tiny helper so the test doesn't need its own import of
// encoding/json twice.
func decodeJSON(data []byte, v any) error {
	return jsonUnmarshal(data, v)
}
