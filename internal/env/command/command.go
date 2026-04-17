package command

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/dakshjotwani/gru/internal/env"
)

const adapterID = "command"

// CreateTimeout is the hard kill deadline for the `create` script. Var (not
// const) so tests can override; production code should treat it as immutable.
var CreateTimeout = 30 * time.Second

// Adapter is the "command" Environment implementation.
type Adapter struct {
	mu        sync.Mutex
	state     map[string]*instanceState // keyed by Instance.ID
	destroyed map[string]bool           // keyed by ProviderRef for post-destroy status
}

type instanceState struct {
	cfg      Config
	spec     env.EnvSpec
	events   *eventPump
	dropped  int
	lastEvtT time.Time
}

// New returns a fresh command adapter.
func New() *Adapter {
	return &Adapter{
		state:     make(map[string]*instanceState),
		destroyed: make(map[string]bool),
	}
}

func (a *Adapter) RuntimeID() string { return adapterID }

// providerRefPayload is the adapter's own envelope around the user-script's
// provider_ref. We wrap it so Rehydrate can reconstruct the same Instance
// even after Gru restart — the wrapper carries the spec so Rehydrate doesn't
// need the caller to re-supply config.
type providerRefPayload struct {
	Version     int            `json:"v"`
	UserRef     string         `json:"user_ref"`
	PtyHolders  []string       `json:"pty_holders"`
	SpecName    string         `json:"spec_name"`
	ConfigJSON  map[string]any `json:"config"`
	Workdirs    []string       `json:"workdirs"`
	SessionID   string         `json:"session_id"`
	ProjectID   string         `json:"project_id,omitempty"`
}

// scriptOutput is what the user's create script emits on stdout.
type scriptOutput struct {
	ProviderRef string   `json:"provider_ref"`
	PtyHolders  []string `json:"pty_holders"`
}

func (a *Adapter) Create(ctx context.Context, spec env.EnvSpec) (env.Instance, error) {
	if spec.Adapter != "" && spec.Adapter != adapterID {
		return env.Instance{}, fmt.Errorf("command: spec.Adapter %q mismatches %q", spec.Adapter, adapterID)
	}
	if len(spec.Workdirs) == 0 {
		return env.Instance{}, fmt.Errorf("command: at least one workdir required")
	}
	cfg, err := parseConfig(spec.Config)
	if err != nil {
		return env.Instance{}, err
	}

	tmplCtx := templateContext{
		SessionID:     spec.Name, // Gru uses the session id as spec.Name at launch
		Workdir:       spec.Workdirs[0],
		Workdirs:      shellEscapeList(spec.Workdirs),
		EnvSpecConfig: jsonOrEmpty(spec.Config),
	}
	createCmd, err := render(cfg.Create, tmplCtx)
	if err != nil {
		return env.Instance{}, err
	}

	// Enforce the 30s hard deadline regardless of caller context.
	createCtx, cancel := context.WithTimeout(ctx, CreateTimeout)
	defer cancel()

	stdoutBuf, stderrBuf, runErr := runShell(createCtx, createCmd, spec.Workdirs[0])
	exitCode := exitCodeFor(runErr)

	out, parseErr := parseScriptOutput(stdoutBuf.Bytes())

	// Resolve the failure table from the spec:
	//   exit 0 + valid JSON + non-empty provider_ref → success
	//   exit 0 + empty stdout                       → fail, no destroy
	//   exit 0 + unparseable stdout                 → fail + destroy with ""
	//   exit 0 + JSON but empty provider_ref        → fail + destroy with ""
	//   exit non-zero                               → fail + destroy with parsed ref if any, else skip
	//   timeout                                     → fail + destroy with ""
	switch {
	case createCtx.Err() == context.DeadlineExceeded:
		a.bestEffortDestroy(ctx, cfg, spec, "")
		return env.Instance{}, fmt.Errorf("command: create timed out after %s; stderr: %s", CreateTimeout, trimShort(stderrBuf.String()))
	case exitCode != 0:
		ref := ""
		if parseErr == nil && out.ProviderRef != "" {
			ref = out.ProviderRef
		}
		if ref != "" {
			a.bestEffortDestroy(ctx, cfg, spec, ref)
		}
		return env.Instance{}, fmt.Errorf("command: create exited %d; stderr: %s", exitCode, trimShort(stderrBuf.String()))
	case len(bytes.TrimSpace(stdoutBuf.Bytes())) == 0:
		return env.Instance{}, fmt.Errorf("command: create produced no stdout JSON; stderr: %s", trimShort(stderrBuf.String()))
	case parseErr != nil:
		a.bestEffortDestroy(ctx, cfg, spec, "")
		return env.Instance{}, fmt.Errorf("command: create stdout last line not valid JSON: %w; stderr: %s", parseErr, trimShort(stderrBuf.String()))
	case out.ProviderRef == "":
		a.bestEffortDestroy(ctx, cfg, spec, "")
		return env.Instance{}, fmt.Errorf("command: create stdout missing provider_ref; stderr: %s", trimShort(stderrBuf.String()))
	}

	ptyHolders := out.PtyHolders
	if len(ptyHolders) == 0 {
		ptyHolders = []string{"tmux"}
	}

	wrapped, err := json.Marshal(providerRefPayload{
		Version:    1,
		UserRef:    out.ProviderRef,
		PtyHolders: ptyHolders,
		SpecName:   spec.Name,
		ConfigJSON: spec.Config,
		Workdirs:   spec.Workdirs,
		SessionID:  spec.Name,
	})
	if err != nil {
		a.bestEffortDestroy(ctx, cfg, spec, out.ProviderRef)
		return env.Instance{}, fmt.Errorf("command: wrap provider ref: %w", err)
	}

	inst := env.Instance{
		ID:          spec.Name,
		Adapter:     adapterID,
		ProviderRef: string(wrapped),
		PtyHolders:  ptyHolders,
		StartedAt:   time.Now().UTC(),
	}

	a.trackInstance(inst.ID, cfg, spec)
	a.startEvents(context.Background(), inst, cfg)
	return inst, nil
}

func (a *Adapter) Rehydrate(ctx context.Context, providerRef string) (env.Instance, error) {
	if providerRef == "" {
		return env.Instance{}, fmt.Errorf("command: empty provider ref")
	}
	var wrapped providerRefPayload
	if err := json.Unmarshal([]byte(providerRef), &wrapped); err != nil {
		return env.Instance{}, fmt.Errorf("command: decode provider ref: %w", err)
	}
	if wrapped.UserRef == "" {
		return env.Instance{}, fmt.Errorf("command: provider ref missing user_ref")
	}

	cfg, err := parseConfig(wrapped.ConfigJSON)
	if err != nil {
		return env.Instance{}, fmt.Errorf("command: rehydrate: %w", err)
	}

	// Probe liveness via the user's status script. If it exits non-zero or
	// prints {"running": false}, treat the backing resource as gone.
	spec := env.EnvSpec{
		Name:     wrapped.SpecName,
		Adapter:  adapterID,
		Config:   wrapped.ConfigJSON,
		Workdirs: wrapped.Workdirs,
	}
	if cfg.Status != "" {
		alive, probeErr := a.probeAlive(ctx, cfg, spec, wrapped.UserRef)
		if probeErr != nil {
			return env.Instance{}, fmt.Errorf("command: status probe on rehydrate: %w", probeErr)
		}
		if !alive {
			return env.Instance{}, fmt.Errorf("command: status script reports backing resource is gone")
		}
	}

	inst := env.Instance{
		ID:          wrapped.SessionID,
		Adapter:     adapterID,
		ProviderRef: providerRef,
		PtyHolders:  wrapped.PtyHolders,
		StartedAt:   time.Now().UTC(),
	}
	a.trackInstance(inst.ID, cfg, spec)
	// Fresh events pump — on Gru restart we need a new subscription.
	a.startEvents(context.Background(), inst, cfg)
	return inst, nil
}

func (a *Adapter) Exec(ctx context.Context, inst env.Instance, cmd []string) (env.ExecResult, error) {
	cfg, spec, userRef, err := a.resolve(inst)
	if err != nil {
		return env.ExecResult{}, err
	}
	rendered, err := renderWithRef(cfg.Exec, spec, userRef)
	if err != nil {
		return env.ExecResult{}, err
	}
	full := rendered + " " + shellEscapeList(cmd)
	stdout, stderr, runErr := runShell(ctx, full, spec.Workdirs[0])
	exit := exitCodeFor(runErr)
	if runErr != nil && !isExitError(runErr) {
		return env.ExecResult{ExitCode: exit, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, fmt.Errorf("command: exec: %w", runErr)
	}
	return env.ExecResult{ExitCode: exit, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

func (a *Adapter) ExecPty(ctx context.Context, inst env.Instance, cmd []string) (io.ReadWriteCloser, error) {
	cfg, spec, userRef, err := a.resolve(inst)
	if err != nil {
		return nil, err
	}
	rendered, err := renderWithRef(cfg.ExecPty, spec, userRef)
	if err != nil {
		return nil, err
	}
	full := rendered + " " + shellEscapeList(cmd)
	c := exec.Command("sh", "-c", full)
	c.Dir = spec.Workdirs[0]
	ptmx, err := pty.Start(c)
	if err != nil {
		return nil, fmt.Errorf("command: exec_pty: %w", err)
	}
	return &ptyHandle{ptmx: ptmx, cmd: c}, nil
}

func (a *Adapter) Destroy(ctx context.Context, inst env.Instance) error {
	a.mu.Lock()
	st, ok := a.state[inst.ID]
	if ok {
		delete(a.state, inst.ID)
		if inst.ProviderRef != "" {
			a.destroyed[inst.ProviderRef] = true
		}
	}
	a.mu.Unlock()
	if ok && st.events != nil {
		st.events.stop()
	}

	cfg, spec, userRef, err := a.resolveRef(inst.ProviderRef)
	if err != nil {
		// Destroy should be resilient: if the provider ref is unparseable,
		// we've already cleared adapter state — consider it done.
		return nil
	}
	return a.runDestroy(ctx, cfg, spec, userRef)
}

func (a *Adapter) Events(ctx context.Context, inst env.Instance) (<-chan env.Event, error) {
	a.mu.Lock()
	st, ok := a.state[inst.ID]
	a.mu.Unlock()
	if !ok || st.events == nil {
		ch := make(chan env.Event)
		close(ch)
		return ch, nil
	}
	return st.events.subscribe(ctx), nil
}

func (a *Adapter) Status(ctx context.Context, inst env.Instance) (env.Status, error) {
	a.mu.Lock()
	_, tracked := a.state[inst.ID]
	destroyed := a.destroyed[inst.ProviderRef]
	a.mu.Unlock()

	if destroyed {
		return env.Status{Running: false}, nil
	}

	cfg, spec, userRef, err := a.resolveRef(inst.ProviderRef)
	if err != nil {
		return env.Status{}, err
	}
	if cfg.Status == "" {
		// No status script — report tracked-so-running.
		return env.Status{Running: tracked}, nil
	}

	rendered, err := renderWithRef(cfg.Status, spec, userRef)
	if err != nil {
		return env.Status{}, err
	}
	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stdout, stderr, runErr := runShell(statusCtx, rendered, spec.Workdirs[0])
	if runErr != nil && !isExitError(runErr) {
		return env.Status{Running: tracked}, fmt.Errorf("command: status script error: %w; stderr: %s", runErr, trimShort(stderr.String()))
	}
	if exitCodeFor(runErr) != 0 {
		return env.Status{Running: false}, nil
	}
	var payload struct {
		Running       bool           `json:"running"`
		DroppedEvents int            `json:"dropped_events"`
		Detail        map[string]any `json:"detail"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		// Malformed JSON: surface running=tracked so callers don't flap.
		return env.Status{Running: tracked, AdapterDetail: map[string]any{"raw": string(stdout.Bytes())}}, nil
	}
	return env.Status{
		Running:       payload.Running,
		DroppedEvents: payload.DroppedEvents,
		AdapterDetail: payload.Detail,
	}, nil
}

// trackInstance stores per-Instance state for later lookup.
func (a *Adapter) trackInstance(id string, cfg Config, spec env.EnvSpec) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state[id] = &instanceState{cfg: cfg, spec: spec}
}

func (a *Adapter) startEvents(ctx context.Context, inst env.Instance, cfg Config) {
	if cfg.Events == "" {
		return
	}
	spec := env.EnvSpec{}
	st, ok := a.state[inst.ID]
	if ok {
		spec = st.spec
	}
	userRef, err := userRefFromProviderRef(inst.ProviderRef)
	if err != nil {
		return
	}
	pump := newEventPump(cfg.Events, spec, userRef)
	a.mu.Lock()
	if st != nil {
		st.events = pump
	}
	a.mu.Unlock()
	go pump.run(ctx)
}

func (a *Adapter) resolve(inst env.Instance) (Config, env.EnvSpec, string, error) {
	return a.resolveRef(inst.ProviderRef)
}

func (a *Adapter) resolveRef(providerRef string) (Config, env.EnvSpec, string, error) {
	if providerRef == "" {
		return Config{}, env.EnvSpec{}, "", fmt.Errorf("command: empty provider ref")
	}
	var wrapped providerRefPayload
	if err := json.Unmarshal([]byte(providerRef), &wrapped); err != nil {
		return Config{}, env.EnvSpec{}, "", fmt.Errorf("command: decode provider ref: %w", err)
	}
	cfg, err := parseConfig(wrapped.ConfigJSON)
	if err != nil {
		return Config{}, env.EnvSpec{}, "", err
	}
	spec := env.EnvSpec{
		Name:     wrapped.SpecName,
		Adapter:  adapterID,
		Config:   wrapped.ConfigJSON,
		Workdirs: wrapped.Workdirs,
	}
	return cfg, spec, wrapped.UserRef, nil
}

func (a *Adapter) runDestroy(ctx context.Context, cfg Config, spec env.EnvSpec, userRef string) error {
	rendered, err := renderWithRef(cfg.Destroy, spec, userRef)
	if err != nil {
		return err
	}
	cwd := "."
	if len(spec.Workdirs) > 0 {
		cwd = spec.Workdirs[0]
	}
	destroyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, stderr, runErr := runShell(destroyCtx, rendered, cwd)
	// Non-zero destroy exits are logged but don't block cleanup.
	if runErr != nil && !isExitError(runErr) {
		return fmt.Errorf("command: destroy: %w (stderr: %s)", runErr, trimShort(stderr.String()))
	}
	return nil
}

func (a *Adapter) bestEffortDestroy(ctx context.Context, cfg Config, spec env.EnvSpec, userRef string) {
	_ = a.runDestroy(ctx, cfg, spec, userRef)
}

func (a *Adapter) probeAlive(ctx context.Context, cfg Config, spec env.EnvSpec, userRef string) (bool, error) {
	rendered, err := renderWithRef(cfg.Status, spec, userRef)
	if err != nil {
		return false, err
	}
	cwd := "."
	if len(spec.Workdirs) > 0 {
		cwd = spec.Workdirs[0]
	}
	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stdout, _, runErr := runShell(statusCtx, rendered, cwd)
	if runErr != nil && !isExitError(runErr) {
		return false, runErr
	}
	if exitCodeFor(runErr) != 0 {
		return false, nil
	}
	var payload struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		// If status is opaque, accept exit 0 as "alive".
		return true, nil
	}
	return payload.Running, nil
}

// ---- shared helpers ----

func runShell(ctx context.Context, cmd, cwd string) (stdout bytes.Buffer, stderr bytes.Buffer, err error) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = cwd
	c.Stdout = &stdout
	c.Stderr = &stderr
	err = c.Run()
	return
}

func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func isExitError(err error) bool {
	if err == nil {
		return true
	}
	_, ok := err.(*exec.ExitError)
	return ok
}

// parseScriptOutput finds the last non-empty line of stdout and decodes it
// as the create-script result JSON.
func parseScriptOutput(stdout []byte) (scriptOutput, error) {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	last := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		last = line
	}
	if err := scanner.Err(); err != nil {
		return scriptOutput{}, err
	}
	if last == "" {
		return scriptOutput{}, fmt.Errorf("empty stdout")
	}
	var out scriptOutput
	if err := json.Unmarshal([]byte(last), &out); err != nil {
		return scriptOutput{}, err
	}
	return out, nil
}

func trimShort(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 512 {
		return s[:512] + "…"
	}
	return s
}

// renderWithRef renders a template and injects provider_ref as the
// {{.ProviderRef}} context variable.
func renderWithRef(tmpl string, spec env.EnvSpec, userRef string) (string, error) {
	ctx := templateContext{
		SessionID:     spec.Name,
		Workdir:       firstWorkdir(spec.Workdirs),
		Workdirs:      shellEscapeList(spec.Workdirs),
		ProviderRef:   userRef,
		EnvSpecConfig: jsonOrEmpty(spec.Config),
	}
	return render(tmpl, ctx)
}

func firstWorkdir(ws []string) string {
	if len(ws) == 0 {
		return ""
	}
	return ws[0]
}

func userRefFromProviderRef(providerRef string) (string, error) {
	var wrapped providerRefPayload
	if err := json.Unmarshal([]byte(providerRef), &wrapped); err != nil {
		return "", err
	}
	return wrapped.UserRef, nil
}

// ptyHandle owns the master pty and its subprocess; Close kills both.
type ptyHandle struct {
	ptmx io.ReadWriteCloser
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
