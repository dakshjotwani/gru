// Package env defines the Environment abstraction: a uniform lifecycle (create,
// exec, destroy) over heterogeneous backing infrastructure — host processes,
// user-supplied shell scripts, etc. See docs/superpowers/specs/2026-04-17-gru-v2-design.md.
package env

import (
	"context"
	"io"
	"time"
)

// Environment is the contract every adapter implements. All methods take a
// context and respect cancellation. Methods may be called concurrently for
// distinct Instances; per-Instance calls are serialized by the caller.
type Environment interface {
	// RuntimeID returns the stable adapter name ("host", "command", ...).
	RuntimeID() string

	// Create a fresh instance for a session. Must return within 30s or error.
	Create(ctx context.Context, spec EnvSpec) (Instance, error)

	// Rehydrate locates a still-live backing resource from a persisted
	// ProviderRef. Returns an Instance with the same semantics as Create().
	// Returns an error if the backing resource is gone.
	Rehydrate(ctx context.Context, providerRef string) (Instance, error)

	// Exec runs a one-shot non-interactive command inside the instance and
	// returns the full result. Safe for short commands only.
	Exec(ctx context.Context, inst Instance, cmd []string) (ExecResult, error)

	// ExecPty runs a command attached to a real pty (TIOCSCTTY-backed). The
	// returned ReadWriteCloser is the master side of the pty. Caller must
	// Close() it to terminate the subprocess.
	ExecPty(ctx context.Context, inst Instance, cmd []string) (io.ReadWriteCloser, error)

	// Destroy tears down the instance. Idempotent — safe to call multiple
	// times (e.g. after a failed Create, after a Gru restart mid-teardown).
	Destroy(ctx context.Context, inst Instance) error

	// Events returns a receive-only channel of lifecycle events. The channel
	// is closed when the instance is destroyed. Backpressure: the adapter
	// is responsible for a bounded channel (capacity 128) and drop-oldest
	// behavior documented in the spec; DroppedEvents is surfaced via Status.
	Events(ctx context.Context, inst Instance) (<-chan Event, error)

	// Status returns a snapshot of the instance's state. Called on-demand.
	Status(ctx context.Context, inst Instance) (Status, error)

	// AgentArgs is called after Create() so the adapter can declare any
	// per-launch flags it wants appended to the agent invocation (e.g.
	// --worktree for the host adapter when config.worktree=true) and
	// optionally override the cwd the agent is launched in (e.g. a
	// command adapter that provisions a fresh clone in its create.sh).
	// A zero AgentArgs value means "no extra args, use spec.Workdirs[0]
	// as the cwd."
	AgentArgs(ctx context.Context, inst Instance) (AgentArgs, error)
}

// AgentArgs is the adapter's per-launch contribution to how the agent
// process is started. Returned from Environment.AgentArgs after Create.
type AgentArgs struct {
	// ExtraArgs are appended to the agent invocation, after the base flags
	// (--model, --agent, etc.) and before the prompt. The strings are
	// passed verbatim; the adapter is responsible for any shell escaping
	// it needs done.
	ExtraArgs []string

	// Cwd overrides the working directory of the agent process. Empty =
	// use spec.Workdirs[0]. Adapters that provision isolated source trees
	// (clones, bind mounts, container volumes) point this at the
	// isolated path.
	Cwd string
}

// EnvSpec is the declarative description of what an adapter should provision.
type EnvSpec struct {
	// Name is a logical identifier the operator chose (e.g. "gru-backend-dev").
	Name string

	// Adapter is the runtime ID of the adapter to use ("host", "command").
	Adapter string

	// Config carries adapter-specific keys. For "command" this holds the
	// five shell command templates; for "host" it is empty.
	Config map[string]any

	// Workdirs is the ordered list of filesystem paths. The first entry is
	// the primary cwd; the rest are passed as --add-dir to Claude Code.
	Workdirs []string

	// Resources is advisory for v2 — adapters may ignore it.
	Resources ResourceLimits

	// SourcePath is the absolute path to the spec YAML file the spec was
	// loaded from. Empty when the spec was built in memory (tests,
	// synthetic specs). The command adapter surfaces this as the
	// {{.SpecDir}} template var so scripts can reference their sibling
	// paths without absolute-path gymnastics.
	SourcePath string
}

// Instance is an adapter-provisioned, live environment. Adapters return it
// from Create()/Rehydrate(); Gru persists ProviderRef and passes Instance
// back into all subsequent calls.
type Instance struct {
	// ID is an opaque Gru-minted identifier (usually the session ID).
	ID string

	// Adapter is the runtime ID of the owning adapter.
	Adapter string

	// ProviderRef is adapter-opaque; persisted by Gru verbatim. On Gru
	// restart, Rehydrate(providerRef) re-locates the backing resource.
	ProviderRef string

	// PtyHolders declares which multiplexers are available inside the
	// instance: "tmux", "dtach", etc. Used by PersistentPty to pick one.
	PtyHolders []string

	// StartedAt is the wall-clock time Create() succeeded.
	StartedAt time.Time
}

// Event is a lifecycle signal the adapter raised. Backpressure is the
// adapter's responsibility; see spec §Events.
type Event struct {
	Kind      string    // "started" | "stopped" | "exit" | "oom" | "disk-full" | "error" | "heartbeat"
	Timestamp time.Time
	Detail    string    // free-form adapter-specific
}

// ResourceLimits is advisory. CPUMilli = millicores (1000 = 1 CPU). 0 means
// unlimited. Adapters are free to ignore.
type ResourceLimits struct {
	CPUMilli int
	MemMB    int
	DiskMB   int
}

// Status is a point-in-time snapshot of an instance's health.
type Status struct {
	Running       bool
	LastEventAt   time.Time
	DroppedEvents int
	ResourceUsage ResourceUsage

	// AdapterDetail is free-form adapter-specific state. For "host" this
	// typically surfaces tmux client count; for "command" it is the raw
	// JSON the user's status.sh printed.
	AdapterDetail map[string]any
}

// ResourceUsage is observed usage (vs. advisory limits). 0 = unknown.
type ResourceUsage struct {
	CPUMilli int
	MemMB    int
	DiskMB   int
}

// ExecResult is the outcome of a one-shot Exec() call.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Known event kinds. Adapters may emit additional kinds in Event.Kind.
const (
	EventStarted   = "started"
	EventStopped   = "stopped"
	EventExit      = "exit"
	EventError     = "error"
	EventOOM       = "oom"
	EventDiskFull  = "disk-full"
	EventHeartbeat = "heartbeat"
)

// EventChannelCapacity is the bounded capacity every adapter MUST use for
// per-instance Event channels. Drop-oldest on overflow is documented in the
// spec; DroppedEvents counts the drops.
const EventChannelCapacity = 128
