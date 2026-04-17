package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSpec(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return p
}

func TestLoadFile_MinimalHost(t *testing.T) {
	dir := t.TempDir()
	// Relative workdir should resolve against the spec file's directory.
	if err := os.MkdirAll(filepath.Join(dir, "workdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := writeSpec(t, dir, "host.yaml", `
name: my-host-env
adapter: host
workdirs:
  - workdir
`)
	spec, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if spec.Name != "my-host-env" {
		t.Errorf("Name = %q, want my-host-env", spec.Name)
	}
	if spec.Adapter != "host" {
		t.Errorf("Adapter = %q, want host", spec.Adapter)
	}
	if len(spec.Workdirs) != 1 || !filepath.IsAbs(spec.Workdirs[0]) {
		t.Errorf("Workdirs[0] should be absolute; got %v", spec.Workdirs)
	}
}

func TestLoadFile_NameDefaultsFromFilename(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := writeSpec(t, dir, "unnamed.yaml", `
adapter: host
workdirs:
  - workdir
`)
	spec, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if spec.Name != "unnamed" {
		t.Errorf("Name = %q, want unnamed", spec.Name)
	}
}

func TestLoadFile_MissingAdapter(t *testing.T) {
	dir := t.TempDir()
	p := writeSpec(t, dir, "bad.yaml", `
workdirs:
  - /tmp
`)
	_, err := LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("expected missing-adapter error, got %v", err)
	}
}

func TestLoadFile_MissingWorkdirs(t *testing.T) {
	dir := t.TempDir()
	p := writeSpec(t, dir, "bad.yaml", `
adapter: host
`)
	_, err := LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), "workdirs") {
		t.Fatalf("expected missing-workdirs error, got %v", err)
	}
}

func TestLoadFile_TildeExpansion(t *testing.T) {
	t.Setenv("HOME", "/tmp/testhome")
	dir := t.TempDir()
	p := writeSpec(t, dir, "tilde.yaml", `
adapter: host
workdirs:
  - ~/myrepo
`)
	spec, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	want := "/tmp/testhome/myrepo"
	if spec.Workdirs[0] != want {
		t.Errorf("Workdirs[0] = %q, want %q", spec.Workdirs[0], want)
	}
}

func TestLoadFile_CommandSpec(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := writeSpec(t, dir, "cmd.yaml", `
name: mini
adapter: command
workdirs:
  - workdir
config:
  mode: frontend
  create: "scripts/create.sh"
`)
	spec, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if spec.Adapter != "command" {
		t.Fatalf("Adapter = %q, want command", spec.Adapter)
	}
	if spec.Config["mode"] != "frontend" {
		t.Errorf("Config[mode] = %v, want frontend", spec.Config["mode"])
	}
	if spec.Config["create"] != "scripts/create.sh" {
		t.Errorf("Config[create] = %v, want scripts/create.sh", spec.Config["create"])
	}
}
