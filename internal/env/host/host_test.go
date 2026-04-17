package host_test

import (
	"crypto/rand"
	"encoding/hex"
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

func randHex() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func TestHostConformance(t *testing.T) {
	adapter := host.New()
	// Use t.TempDir at suite setup — each NewSpec call returns a fresh dir.
	conformance.Run(t, conformance.Suite{
		Name:    "host",
		Adapter: adapter,
		NewSpec: func(r conformance.Reporter) env.EnvSpec {
			dir := t.TempDir()
			return env.EnvSpec{
				Name:     "host-conf-" + time.Now().Format("150405") + "-" + randHex(),
				Adapter:  "host",
				Workdirs: []string{dir},
			}
		},
		KillBackingResource: func(r conformance.Reporter, inst env.Instance) {
			dir, err := workdirFromProviderRef(inst.ProviderRef)
			if err != nil {
				r.Fatalf("decode provider ref: %v", err)
				return
			}
			if err := os.RemoveAll(dir); err != nil {
				r.Fatalf("remove workdir: %v", err)
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
