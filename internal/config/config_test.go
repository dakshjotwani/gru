package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dakshjotwani/gru/internal/config"
)

func TestLoad_defaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(filepath.Join(dir, "server.yaml"))
	if err != nil {
		t.Fatalf("Load returned error for missing file: %v", err)
	}
	if cfg.Addr != ":7777" {
		t.Errorf("default addr = %q, want %q", cfg.Addr, ":7777")
	}
	if cfg.Bind != "tailnet" {
		t.Errorf("default bind = %q, want %q", cfg.Bind, "tailnet")
	}
}

func TestLoad_ignoresLegacyAPIKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	content := "addr: \":8080\"\napi_key: \"old-key\"\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("addr = %q, want %q", cfg.Addr, ":8080")
	}
	if cfg.Bind != "tailnet" {
		t.Errorf("default bind should fill in to tailnet when absent, got %q", cfg.Bind)
	}
}

func TestLoad_fromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")
	content := "addr: \":9090\"\nbind: \"loopback\"\ndb_path: \"/tmp/gru.db\"\n"
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
	if cfg.Bind != "loopback" {
		t.Errorf("bind = %q, want %q", cfg.Bind, "loopback")
	}
	if cfg.DBPath != "/tmp/gru.db" {
		t.Errorf("db_path = %q, want %q", cfg.DBPath, "/tmp/gru.db")
	}
}
