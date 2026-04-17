package command

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/dakshjotwani/gru/internal/env"
)

// Tunables. These are vars (not const) so tests can override them; production
// code should treat them as immutable.
var (
	// HeartbeatTimeout is the maximum silence we tolerate from an events
	// script before synthesizing an error and respawning.
	HeartbeatTimeout = 120 * time.Second

	// RespawnBackoff is the wait between a crashed events script and the
	// next respawn attempt.
	RespawnBackoff = 5 * time.Second

	// RespawnWindow is the rolling window over which restarts are counted.
	RespawnWindow = 5 * time.Minute

	// RespawnLimit is the maximum restarts within RespawnWindow before we
	// give up.
	RespawnLimit = 3
)

// eventPump runs the user's events script, relays its output to subscribers
// through a bounded channel, and enforces heartbeat + respawn policy.
type eventPump struct {
	tmpl    string
	spec    env.EnvSpec
	userRef string

	mu        sync.Mutex
	subs      []chan env.Event
	closed    bool
	dropped   int
	lastEvent time.Time

	stopCh chan struct{}
}

func newEventPump(tmpl string, spec env.EnvSpec, userRef string) *eventPump {
	return &eventPump{tmpl: tmpl, spec: spec, userRef: userRef, stopCh: make(chan struct{})}
}

func (p *eventPump) subscribe(ctx context.Context) <-chan env.Event {
	ch := make(chan env.Event, env.EventChannelCapacity)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		close(ch)
		return ch
	}
	p.subs = append(p.subs, ch)
	p.mu.Unlock()
	go func() {
		<-ctx.Done()
		p.unsubscribe(ch)
	}()
	return ch
}

func (p *eventPump) unsubscribe(ch chan env.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.subs {
		if s == ch {
			p.subs = append(p.subs[:i], p.subs[i+1:]...)
			close(s)
			return
		}
	}
}

func (p *eventPump) droppedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

func (p *eventPump) stop() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.stopCh)
	subs := p.subs
	p.subs = nil
	p.mu.Unlock()
	for _, s := range subs {
		close(s)
	}
}

// run is the main loop. It spawns the events script, scans its stdout,
// synthesizes heartbeat-timeout errors, and handles respawn within the
// window. It exits when stop() is called or the respawn limit is exceeded.
func (p *eventPump) run(ctx context.Context) {
	restartTimes := make([]time.Time, 0, RespawnLimit+1)
	for {
		select {
		case <-p.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		rendered, err := renderWithRef(p.tmpl, p.spec, p.userRef)
		if err != nil {
			p.emit(env.Event{
				Kind: env.EventError, Timestamp: time.Now().UTC(),
				Detail: fmt.Sprintf("events template render: %v", err),
			})
			return
		}

		cwd := "."
		if len(p.spec.Workdirs) > 0 {
			cwd = p.spec.Workdirs[0]
		}
		exitCode, stderr := p.streamOnce(ctx, rendered, cwd)

		select {
		case <-p.stopCh:
			return
		default:
		}

		p.emit(env.Event{
			Kind: env.EventError, Timestamp: time.Now().UTC(),
			Detail: fmt.Sprintf("events script exited (code=%d): %s", exitCode, trimShort(stderr)),
		})

		now := time.Now()
		restartTimes = pruneBefore(restartTimes, now.Add(-RespawnWindow))
		if len(restartTimes) >= RespawnLimit {
			p.emit(env.Event{
				Kind: env.EventError, Timestamp: now.UTC(),
				Detail: fmt.Sprintf("events script exceeded respawn limit (%d in %s); giving up", RespawnLimit, RespawnWindow),
			})
			return
		}
		restartTimes = append(restartTimes, now)

		select {
		case <-time.After(RespawnBackoff):
		case <-p.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// streamOnce runs the events script once. Every line of stdout is parsed as a
// JSON Event and emitted. On HeartbeatTimeout silence, synthesizes an error
// event and kills the subprocess so the caller respawns it.
func (p *eventPump) streamOnce(ctx context.Context, shellCmd, cwd string) (exitCode int, stderr string) {
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := exec.CommandContext(subCtx, "sh", "-c", shellCmd)
	c.Dir = cwd
	stdout, err := c.StdoutPipe()
	if err != nil {
		return -1, err.Error()
	}
	errPipe, err := c.StderrPipe()
	if err != nil {
		return -1, err.Error()
	}
	if err := c.Start(); err != nil {
		return -1, err.Error()
	}

	// Heartbeat watchdog: if no line in HeartbeatTimeout, kill the process.
	silenceTimer := time.NewTimer(HeartbeatTimeout)
	lineCh := make(chan []byte)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := make([]byte, len(scanner.Bytes()))
			copy(line, scanner.Bytes())
			select {
			case lineCh <- line:
			case <-subCtx.Done():
				return
			}
		}
	}()

	var stderrBuf []byte
	go func() {
		b, _ := io.ReadAll(errPipe)
		stderrBuf = b
	}()

	var lastEmit time.Time
loop:
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				break loop
			}
			silenceTimer.Stop()
			silenceTimer = time.NewTimer(HeartbeatTimeout)
			evt, ok := decodeEventLine(line)
			if !ok {
				// Garbled line — surface as a single error event.
				p.emit(env.Event{
					Kind: env.EventError, Timestamp: time.Now().UTC(),
					Detail: fmt.Sprintf("events script emitted non-JSON line: %s", trimShort(string(line))),
				})
				continue
			}
			if evt.Timestamp.IsZero() {
				evt.Timestamp = time.Now().UTC()
			}
			// Don't retain heartbeat lines past the next emission;
			// consumers use Status for heartbeat visibility, not the event
			// channel itself. But we still emit heartbeat once so the
			// channel reflects liveness if a subscriber wants it.
			p.emit(evt)
			lastEmit = time.Now()
		case <-silenceTimer.C:
			p.emit(env.Event{
				Kind: env.EventError, Timestamp: time.Now().UTC(),
				Detail: fmt.Sprintf("events stream stalled (no output for %s since %s)", HeartbeatTimeout, lastEmit.Format(time.RFC3339)),
			})
			cancel()
			break loop
		case <-p.stopCh:
			cancel()
			break loop
		case <-ctx.Done():
			break loop
		case <-done:
			break loop
		}
	}

	_ = c.Wait()
	exitCode = c.ProcessState.ExitCode()
	return exitCode, string(stderrBuf)
}

// emit delivers evt to all subscribers. Drop-oldest on overflow, and
// heartbeats are coalesced so they don't fill the buffer.
func (p *eventPump) emit(evt env.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.lastEvent = evt.Timestamp
	for _, ch := range p.subs {
		// Coalesce heartbeats: drain any pending heartbeat so we never keep
		// more than the latest one in the buffer.
		if evt.Kind == env.EventHeartbeat {
			select {
			case prev := <-ch:
				if prev.Kind != env.EventHeartbeat {
					// We stole a non-heartbeat event; put it back at head.
					// If the channel is full, we'd loop — accept a rare
					// drop here rather than block.
					select {
					case ch <- prev:
					default:
						p.dropped++
					}
				}
			default:
			}
		}
		select {
		case ch <- evt:
		default:
			p.dropped++
		}
	}
}

func decodeEventLine(line []byte) (env.Event, bool) {
	var raw struct {
		Kind      string    `json:"kind"`
		Timestamp time.Time `json:"timestamp"`
		Detail    string    `json:"detail"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return env.Event{}, false
	}
	if raw.Kind == "" {
		return env.Event{}, false
	}
	return env.Event{Kind: raw.Kind, Timestamp: raw.Timestamp, Detail: raw.Detail}, true
}

func pruneBefore(times []time.Time, cutoff time.Time) []time.Time {
	out := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}
