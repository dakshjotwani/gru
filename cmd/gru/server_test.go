package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritePortFile_Atomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.port")

	if err := writePortFile(path, 54321); err != nil {
		t.Fatalf("writePortFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read port file: %v", err)
	}
	want := "127.0.0.1:54321\n"
	if string(data) != want {
		t.Errorf("port file contents = %q, want %q", data, want)
	}

	// Overwriting must succeed and not leave the tmp file behind.
	if err := writePortFile(path, 11111); err != nil {
		t.Fatalf("writePortFile overwrite: %v", err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "11111") {
		t.Errorf("after overwrite, file = %q, want it to contain 11111", data)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file lingered after successful rename: err=%v", err)
	}
}

func TestStateDir_EnvOverride(t *testing.T) {
	t.Setenv("GRU_STATE_DIR", "/tmp/minion-foo")
	if got := stateDir(); got != "/tmp/minion-foo" {
		t.Errorf("stateDir() = %q, want %q", got, "/tmp/minion-foo")
	}
}

func TestStateDir_DefaultsToHomeGru(t *testing.T) {
	t.Setenv("GRU_STATE_DIR", "")
	t.Setenv("HOME", "/tmp/fake-home")
	want := "/tmp/fake-home/.gru"
	if got := stateDir(); got != want {
		t.Errorf("stateDir() = %q, want %q", got, want)
	}
}
