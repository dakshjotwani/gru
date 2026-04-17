package host_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/env/conformance"
	"github.com/dakshjotwani/gru/internal/env/host"
)

func TestHostConformance(t *testing.T) {
	adapter := host.New()
	conformance.Run(t, conformance.Suite{
		Name:    "host",
		Adapter: adapter,
		NewSpec: func(t *testing.T) env.EnvSpec {
			dir := t.TempDir()
			return env.EnvSpec{
				Name:     "host-conf-" + t.Name() + "-" + time.Now().Format("150405.000"),
				Adapter:  "host",
				Workdirs: []string{dir},
			}
		},
		KillBackingResource: func(t *testing.T, inst env.Instance) {
			dir, err := workdirFromProviderRef(inst.ProviderRef)
			if err != nil {
				t.Fatalf("decode provider ref: %v", err)
			}
			if err := os.RemoveAll(dir); err != nil {
				t.Fatalf("remove workdir: %v", err)
			}
		},
		ForceLifecycleEvent:   nil,
		SupportsEventsRespawn: false,
	})
}

// workdirFromProviderRef decodes the host adapter's provider-ref JSON. The
// conformance layer treats ProviderRef as opaque, so adapter-specific tests
// reach in here.
func workdirFromProviderRef(ref string) (string, error) {
	var raw struct {
		Workdirs []string `json:"workdirs"`
	}
	if err := json.Unmarshal([]byte(ref), &raw); err != nil {
		return "", err
	}
	if len(raw.Workdirs) == 0 {
		return "", errors.New("no workdirs in provider ref")
	}
	return filepath.Clean(raw.Workdirs[0]), nil
}
