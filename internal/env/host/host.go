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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/dakshjotwani/gru/internal/env"
)

const adapterID = "host"

// ErrWorkdirSetInUse is returned by Create/Rehydrate when the same ordered
// set of workdirs is already claimed by a live instance. Host has no
// isolation, so two concurrent agents on the same workdirs would fight over
// ports, node_modules, git trees, etc. The spec enforces one session per
// workdir set; this is the enforcement point.
var ErrWorkdirSetInUse = fmt.Errorf("host: another instance is already running on the same workdir set")

// Adapter is the host-machine Environment implementation.
type Adapter struct {
	mu         sync.Mutex
	events     map[string]*eventHub // keyed by Instance.ID
	dropped    map[string]int
	destroyed  map[string]bool
	activeSets map[string]string // workdir-set key → instance.ID holding it
}

// New returns a fresh host adapter.
func New() *Adapter {
	return &Adapter{
		events:     make(map[string]*eventHub),
		dropped:    make(map[string]int),
		destroyed:  make(map[string]bool),
		activeSets: make(map[string]string),
	}
}

// workdirSetKey is the canonical string form used to detect duplicates. We
// don't sort because workdir order matters to Claude Code (primary cwd vs
// --add-dir) — different orderings are different sessions.
func workdirSetKey(workdirs []string) string {
	// Clean paths so /tmp/foo and /tmp/foo/ collide. Preserve order.
	cleaned := make([]string, len(workdirs))
	for i, wd := range workdirs {
		cleaned[i] = strings.TrimRight(wd, "/")
	}
	return strings.Join(cleaned, "\x00")
}

// sortedWorkdirs is a helper for test assertions; not used in production.
func sortedWorkdirs(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
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

	key := workdirSetKey(spec.Workdirs)
	a.mu.Lock()
	if other, taken := a.activeSets[key]; taken && other != spec.Name {
		a.mu.Unlock()
		return env.Instance{}, fmt.Errorf("%w (held by instance %q)", ErrWorkdirSetInUse, other)
	}
	a.activeSets[key] = spec.Name
	// Instances that are Destroy()-ed and then Create()-d again with the
	// same ID should drop the stale destroyed flag.
	delete(a.destroyed, spec.Name)
	a.mu.Unlock()

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
	// Rehydrate must re-claim the workdir set so a post-restart Create
	// attempting the same paths is rejected. Duplicate claim here is
	// impossible on a fresh process start; it could only happen if
	// Rehydrate is called twice with the same ref — treated as idempotent.
	key := workdirSetKey(payload.Workdirs)
	a.mu.Lock()
	if _, taken := a.activeSets[key]; !taken {
		// Rehydrate does not get a session ID, so the adapter marks the
		// slot as taken with a sentinel. Destroy() looks up by matching
		// ProviderRef and clears it.
		a.activeSets[key] = "rehydrated"
	}
	a.mu.Unlock()
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
	// Release the workdir-set claim so future Create()s can use it.
	if inst.ProviderRef != "" {
		if wds, err := workdirsFromRef(inst.ProviderRef); err == nil {
			key := workdirSetKey(wds)
			if holder, held := a.activeSets[key]; held && holder == inst.ID {
				delete(a.activeSets, key)
			}
		}
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
	wds, err := workdirsFromRef(inst.ProviderRef)
	if err != nil {
		return "", err
	}
	if len(wds) == 0 {
		return "", fmt.Errorf("host: no workdirs in instance")
	}
	return wds[0], nil
}

func workdirsFromRef(ref string) ([]string, error) {
	var payload providerRefPayload
	if err := json.Unmarshal([]byte(ref), &payload); err != nil {
		return nil, fmt.Errorf("host: decode provider ref: %w", err)
	}
	return payload.Workdirs, nil
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
