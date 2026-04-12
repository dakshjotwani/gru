package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dakshjotwani/gru/internal/config"
)

func TestLoad_defaults(t *testing.T) {
	// No config file — should return defaults.
	cfg, err := config.Load("/nonexistent/path/server.yaml")
	if err != nil {
		t.Fatalf("Load returned error for missing file: %v", err)
	}
	if cfg.Addr != ":7777" {
		t.Errorf("default addr = %q, want %q", cfg.Addr, ":7777")
	}
	if cfg.APIKey == "" {
		t.Error("default APIKey should not be empty (auto-generated)")
	}
}

func TestLoad_fromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	content := "addr: \":9090\"\napi_key: \"test-key-123\"\ndb_path: \"/tmp/gru.db\"\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":9090" {
		t.Errorf("addr = %q, want %q", cfg.Addr, ":9090")
	}
	if cfg.APIKey != "test-key-123" {
		t.Errorf("api_key = %q, want %q", cfg.APIKey, "test-key-123")
	}
}
