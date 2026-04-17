package persistentpty_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"testing"
	"time"

	"github.com/dakshjotwani/gru/internal/env"
	"github.com/dakshjotwani/gru/internal/env/host"
	"github.com/dakshjotwani/gru/internal/env/persistentpty"
)

func randHex(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

func TestPersistentPty_StartAttachStop(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed; skipping persistent pty test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a := host.New()
	inst, err := a.Create(ctx, env.EnvSpec{
		Name:     "pty-test-" + time.Now().Format("150405.000"),
		Adapter:  "host",
		Workdirs: []string{t.TempDir()},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = a.Destroy(ctx, inst) }()

	p := persistentpty.New()
	name := "gru-pty-test-" + time.Now().Format("150405") + "-" + randHex(t)
	// Clean up any leftover session from a previous flake run.
	defer func() { _ = p.Stop(ctx, a, inst, name) }()

	// Start a tmux session that just runs `sleep 30` so it's alive for the
	// attach/status checks.
	if err := p.Start(ctx, a, inst, name, "", "sleep 30"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	st, err := p.Status(ctx, a, inst, name)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Exists {
		t.Fatalf("Status.Exists=false after Start; expected true")
	}

	// Start is idempotent when the session already exists.
	if err := p.Start(ctx, a, inst, name, "", "sleep 30"); err != nil {
		t.Fatalf("Start (idempotent): %v", err)
	}

	stream, err := p.Attach(ctx, a, inst, name)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	buf := make([]byte, 64)
	readDone := make(chan struct{})
	go func() {
		_, _ = stream.Read(buf)
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		// Not strictly required — tmux may produce no output immediately.
	}
	_ = stream.Close()

	if err := p.Stop(ctx, a, inst, name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Stop is idempotent: calling twice returns nil.
	if err := p.Stop(ctx, a, inst, name); err != nil {
		t.Fatalf("Stop (idempotent): %v", err)
	}

	st, err = p.Status(ctx, a, inst, name)
	if err != nil {
		t.Fatalf("Status after Stop: %v", err)
	}
	if st.Exists {
		t.Fatalf("Status.Exists=true after Stop; expected false")
	}
}
