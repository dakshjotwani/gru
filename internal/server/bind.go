package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"time"
)

// ResolveBindAddrs returns the concrete listen addresses for a given bind
// mode and port. A single server startup may produce multiple listeners
// (e.g. loopback + tailscale interface).
//
// mode:
//
//	"loopback" → 127.0.0.1:<port> only
//	"tailnet"  → 127.0.0.1:<port> + <tailscale-ipv4>:<port> when Tailscale
//	             is detected; falls back to loopback with a warning if not
//	"all"      → 0.0.0.0:<port>; logs a loud warning — no app-level auth,
//	             so exposing on all interfaces is on-you
//
// port may be "0" for ephemeral binding (tests). In that case the caller
// is responsible for reading the actual bound port off the first listener
// and using it to open subsequent ones — this helper just substitutes the
// requested port into each address string.
func ResolveBindAddrs(ctx context.Context, mode, port string) ([]string, error) {
	if port == "" {
		return nil, fmt.Errorf("resolve bind: empty port")
	}
	switch mode {
	case "loopback":
		return []string{"127.0.0.1:" + port}, nil
	case "all":
		log.Printf("warning: server.bind=all — no app-level auth is configured; " +
			"anyone who can reach this port can drive Gru. Use bind: tailnet instead.")
		return []string{"0.0.0.0:" + port}, nil
	case "", "tailnet":
		addrs := []string{"127.0.0.1:" + port}
		ts, err := tailscaleIPv4(ctx)
		if err != nil {
			log.Printf("warning: tailscale not detected (%v) — falling back to loopback only", err)
			return addrs, nil
		}
		addrs = append(addrs, ts+":"+port)
		return addrs, nil
	default:
		return nil, fmt.Errorf("unknown server.bind mode %q (want loopback|tailnet|all)", mode)
	}
}

// tailscaleIPv4 invokes `tailscale ip -4` with a short timeout and returns
// the first IPv4 address it prints. Returns an error if the command fails
// or produces no usable address.
func tailscaleIPv4(ctx context.Context) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "tailscale", "ip", "-4").Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if ip := net.ParseIP(line); ip != nil && ip.To4() != nil {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("tailscale ip -4 produced no IPv4 address")
}
