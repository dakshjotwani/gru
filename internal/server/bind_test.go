package server_test

import (
	"context"
	"testing"

	"github.com/dakshjotwani/gru/internal/server"
)

func TestResolveBindAddrs_loopback(t *testing.T) {
	addrs, err := server.ResolveBindAddrs(context.Background(), "loopback", "7777")
	if err != nil {
		t.Fatalf("ResolveBindAddrs: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != "127.0.0.1:7777" {
		t.Errorf("got %v, want [127.0.0.1:7777]", addrs)
	}
}

func TestResolveBindAddrs_all(t *testing.T) {
	addrs, err := server.ResolveBindAddrs(context.Background(), "all", "7777")
	if err != nil {
		t.Fatalf("ResolveBindAddrs: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != "0.0.0.0:7777" {
		t.Errorf("got %v, want [0.0.0.0:7777]", addrs)
	}
}

func TestResolveBindAddrs_tailnetFallsBack(t *testing.T) {
	// "tailnet" mode should return at least loopback even when tailscale is
	// absent (it falls back with a warning). We can't reliably assert the
	// tailscale IP is present because the test host may or may not be on a
	// tailnet — but loopback must always be first.
	addrs, err := server.ResolveBindAddrs(context.Background(), "tailnet", "7777")
	if err != nil {
		t.Fatalf("ResolveBindAddrs: %v", err)
	}
	if len(addrs) < 1 || addrs[0] != "127.0.0.1:7777" {
		t.Errorf("got %v, want [127.0.0.1:7777, ...]", addrs)
	}
}

func TestResolveBindAddrs_empty_defaults_to_tailnet(t *testing.T) {
	addrs, err := server.ResolveBindAddrs(context.Background(), "", "7777")
	if err != nil {
		t.Fatalf("ResolveBindAddrs: %v", err)
	}
	if len(addrs) < 1 || addrs[0] != "127.0.0.1:7777" {
		t.Errorf("got %v, want loopback first, then maybe tailscale", addrs)
	}
}

func TestResolveBindAddrs_unknown(t *testing.T) {
	_, err := server.ResolveBindAddrs(context.Background(), "bogus", "7777")
	if err == nil {
		t.Fatal("expected error for unknown mode, got nil")
	}
}

func TestResolveBindAddrs_emptyPort(t *testing.T) {
	_, err := server.ResolveBindAddrs(context.Background(), "loopback", "")
	if err == nil {
		t.Fatal("expected error for empty port, got nil")
	}
}
