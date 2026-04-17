// Package host implements the "host" Environment adapter: runs commands
// directly on the operator's machine with no sandboxing or provisioning.
// Isolation is explicitly the operator's problem here — two host instances
// share the same process tree, filesystem, and port space.
package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/dakshjotwani/gru/internal/env"
)

const adapterID = "host"

// Adapter is the host-machine Environment implementation.
type Adapter struct {
	mu        sync.Mutex
	events    map[string]*eventHub // keyed by Instance.ID
	dropped   map[string]int
	destroyed map[string]bool
}

// New returns a fresh host adapter.
func New() *Adapter {
	return &Adapter{
		events:    make(map[string]*eventHub),
		dropped:   make(map[string]int),
		destroyed: make(map[string]bool),
	}
}

func (a *Adapter) RuntimeID() string { return adapterID }

// providerRefPayload is what we serialize into ProviderRef. Keeping it as JSON
// lets us evolve the schema without breaking on-disk sessions.
type providerRefPayload struct {
	Workdirs []string `json:"workdirs"`
}

func (a *Adapter) Create(ctx context.Context, spec env.EnvSpec) (env.Instance, error) {
	if spec.Adapter != "" && spec.Adapter != adapterID {
		return env.Instance{}, fmt.Errorf("host: spec.Adapter %q mismatches %q", spec.Adapter, adapterID)
	}
	if len(spec.Workdirs) == 0 {
		return env.Instance{}, fmt.Errorf("host: at least one workdir required")
	}
	for _, wd := range spec.Workdirs {
		st, err := os.Stat(wd)
		if err != nil {
			return env.Instance{}, fmt.Errorf("host: workdir %q: %w", wd, err)
		}
		if !st.IsDir() {
			return env.Instance{}, fmt.Errorf("host: workdir %q is not a directory", wd)
		}
	}

	refBytes, err := json.Marshal(providerRefPayload{Workdirs: spec.Workdirs})
	if err != nil {
		return env.Instance{}, fmt.Errorf("host: marshal provider ref: %w", err)
	}

	inst := env.Instance{
		ID:          spec.Name,
		Adapter:     adapterID,
		ProviderRef: string(refBytes),
		PtyHolders:  []string{"tmux"},
		StartedAt:   time.Now().UTC(),
	}

	hub := a.ensureHub(inst.ID)
	hub.emit(env.Event{Kind: env.EventStarted, Timestamp: time.Now().UTC(), Detail: "host instance created"})
	return inst, nil
}

func (a *Adapter) Rehydrate(ctx context.Context, providerRef string) (env.Instance, error) {
	if providerRef == "" {
		return env.Instance{}, fmt.Errorf("host: empty provider ref")
	}
	var payload providerRefPayload
	if err := json.Unmarshal([]byte(providerRef), &payload); err != nil {
		return env.Instance{}, fmt.Errorf("host: decode provider ref: %w", err)
	}
	if len(payload.Workdirs) == 0 {
		return env.Instance{}, fmt.Errorf("host: no workdirs in provider ref")
	}
	for _, wd := range payload.Workdirs {
		if st, err := os.Stat(wd); err != nil || !st.IsDir() {
			return env.Instance{}, fmt.Errorf("host: workdir %q missing on rehydrate", wd)
		}
	}
	return env.Instance{
		Adapter:     adapterID,
		ProviderRef: providerRef,
		PtyHolders:  []string{"tmux"},
		StartedAt:   time.Now().UTC(),
	}, nil
}

func (a *Adapter) Exec(ctx context.Context, inst env.Instance, cmd []string) (env.ExecResult, error) {
	if len(cmd) == 0 {
		return env.ExecResult{}, fmt.Errorf("host: empty command")
	}
	wd, err := primaryWorkdir(inst)
	if err != nil {
		return env.ExecResult{}, err
	}
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Dir = wd
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	runErr := c.Run()
	res := env.ExecResult{
		ExitCode: c.ProcessState.ExitCode(),
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}
	// exec.Command returns an *ExitError for non-zero exit; surface the exit
	// code but do not treat it as a Go error — callers inspect ExitCode.
	if _, isExit := runErr.(*exec.ExitError); runErr != nil && !isExit {
		return res, fmt.Errorf("host: exec %v: %w", cmd, runErr)
	}
	return res, nil
}

func (a *Adapter) ExecPty(ctx context.Context, inst env.Instance, cmd []string) (io.ReadWriteCloser, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("host: empty command")
	}
	wd, err := primaryWorkdir(inst)
	if err != nil {
		return nil, err
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Dir = wd
	ptmx, err := pty.Start(c)
	if err != nil {
		return nil, fmt.Errorf("host: pty start %v: %w", cmd, err)
	}
	// Caller owns Close(); when they do, kill the subprocess.
	return &ptyHandle{ptmx: ptmx, cmd: c}, nil
}

func (a *Adapter) Destroy(ctx context.Context, inst env.Instance) error {
	a.mu.Lock()
	hub, ok := a.events[inst.ID]
	if ok {
		delete(a.events, inst.ID)
	}
	if inst.ID != "" {
		a.destroyed[inst.ID] = true
	}
	a.mu.Unlock()
	if hub != nil {
		hub.emit(env.Event{Kind: env.EventStopped, Timestamp: time.Now().UTC(), Detail: "host instance destroyed"})
		hub.close()
	}
	return nil
}

func (a *Adapter) Events(ctx context.Context, inst env.Instance) (<-chan env.Event, error) {
	hub := a.ensureHub(inst.ID)
	return hub.subscribe(ctx), nil
}

func (a *Adapter) Status(ctx context.Context, inst env.Instance) (env.Status, error) {
	wd, err := primaryWorkdir(inst)
	if err != nil {
		return env.Status{}, err
	}
	a.mu.Lock()
	destroyed := a.destroyed[inst.ID]
	dropped := a.dropped[inst.ID]
	hub, hasHub := a.events[inst.ID]
	a.mu.Unlock()
	running := !destroyed
	if running {
		if st, err := os.Stat(wd); err != nil || !st.IsDir() {
			running = false
		}
	}
	last := time.Time{}
	if hasHub {
		last = hub.lastEventAt()
	}
	return env.Status{
		Running:       running,
		LastEventAt:   last,
		DroppedEvents: dropped,
		AdapterDetail: map[string]any{"workdir": wd},
	}, nil
}

func primaryWorkdir(inst env.Instance) (string, error) {
	var payload providerRefPayload
	if err := json.Unmarshal([]byte(inst.ProviderRef), &payload); err != nil {
		return "", fmt.Errorf("host: decode provider ref: %w", err)
	}
	if len(payload.Workdirs) == 0 {
		return "", fmt.Errorf("host: no workdirs in instance")
	}
	return payload.Workdirs[0], nil
}

func (a *Adapter) ensureHub(id string) *eventHub {
	a.mu.Lock()
	defer a.mu.Unlock()
	h, ok := a.events[id]
	if !ok {
		h = newEventHub(func() { a.mu.Lock(); a.dropped[id]++; a.mu.Unlock() })
		a.events[id] = h
	}
	return h
}

// ptyHandle owns the master pty and the subprocess; closing it kills both.
type ptyHandle struct {
	ptmx *os.File
	cmd  *exec.Cmd
	once sync.Once
}

func (h *ptyHandle) Read(p []byte) (int, error)  { return h.ptmx.Read(p) }
func (h *ptyHandle) Write(p []byte) (int, error) { return h.ptmx.Write(p) }
func (h *ptyHandle) Close() error {
	var err error
	h.once.Do(func() {
		err = h.ptmx.Close()
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
			_ = h.cmd.Wait()
		}
	})
	return err
}
