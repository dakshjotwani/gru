// Package spec loads env.EnvSpec values from YAML files. Shared between the
// gru CLI (`gru env test`, `gru launch --env-spec`) and the gRPC server's
// LaunchSession handler so both consumers parse the same format.
package spec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dakshjotwani/gru/internal/env"
)

// File is the on-disk YAML representation of an env.EnvSpec.
type File struct {
	Name     string         `yaml:"name"`
	Adapter  string         `yaml:"adapter"`
	Workdirs []string       `yaml:"workdirs"`
	Config   map[string]any `yaml:"config"`
}

// LoadFile reads an env spec from path. Relative workdirs are resolved against
// the spec file's directory; a missing Name defaults to the file's basename
// (sans extension).
func LoadFile(path string) (env.EnvSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return env.EnvSpec{}, fmt.Errorf("read spec file: %w", err)
	}
	var sf File
	if err := yaml.Unmarshal(data, &sf); err != nil {
		return env.EnvSpec{}, fmt.Errorf("parse spec file %s: %w", path, err)
	}
	if sf.Adapter == "" {
		return env.EnvSpec{}, fmt.Errorf("spec file %s is missing 'adapter'", path)
	}
	if !env.IsKnownAdapter(sf.Adapter) {
		return env.EnvSpec{}, fmt.Errorf("spec file %s: unknown adapter %q; known: %s",
			path, sf.Adapter, strings.Join(env.KnownAdapterIDs, ", "))
	}
	if len(sf.Workdirs) == 0 {
		return env.EnvSpec{}, fmt.Errorf("spec file %s is missing 'workdirs' (need at least one)", path)
	}
	specDir := filepath.Dir(path)
	for i, wd := range sf.Workdirs {
		wd = expandHome(wd)
		if !filepath.IsAbs(wd) {
			wd = filepath.Join(specDir, wd)
		}
		sf.Workdirs[i] = wd
	}
	name := sf.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return env.EnvSpec{
		Name:       name,
		Adapter:    sf.Adapter,
		Workdirs:   sf.Workdirs,
		Config:     sf.Config,
		SourcePath: abs,
	}, nil
}

// expandHome turns "~" or "~/x" into "$HOME/x". Leaves everything else alone.
// Keeping this local avoids a dep on a shell-expand library for a one-case need.
func expandHome(p string) string {
	if p == "~" {
		return os.Getenv("HOME")
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(os.Getenv("HOME"), p[2:])
	}
	return p
}
