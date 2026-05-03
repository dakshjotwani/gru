// Package tailer is the producer side of Gru's state pipeline rev 3:
// per-session goroutines that tail the gru-owned event log
// (~/.gru/events/<sid>.jsonl), apply the state-derivation function,
// and write the resulting state to SQLite in a single transaction.
// See docs/adr/0002-rev3-hook-driven-event-log.md.
//
// Replay-from-zero is the central correctness property: on (re)start,
// each tailer wipes its session's events-projection cache and re-reads
// the log from byte 0. The byte offset never leaves RAM.
package tailer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/dakshjotwani/gru/internal/ingest"
	"github.com/dakshjotwani/gru/internal/state"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
)

// Notifier is the publisher-side hook the tailer signals after every
// commit. Single-method interface so the tailer doesn't import the
// publisher package and so tests can inject a counting fake.
type Notifier interface {
	Notify(sessionID string)
}

type noopNotifier struct{}

func (noopNotifier) Notify(string) {}

// Config wires a Tailer to its inputs. Only EventsPath is required;
// all other fields have sensible defaults.
type Config struct {
	SessionID  string
	ProjectID  string
	Runtime    string
	EventsPath string // ~/.gru/events/<sid>.jsonl

	Store    *store.Store
	Notifier Notifier

	// PollInterval is the fallback poll cadence used when fsnotify is
	// unavailable. Defaults to 250 ms.
	PollInterval time.Duration

	// UseFsnotify, when false, forces the tailer onto its polling
	// code path. Tests use this to verify the fallback works.
	UseFsnotify *bool

	Logger *log.Logger
}

// Tailer is one running session-tailer goroutine. Don't share across
// sessions — each instance owns its own offset and partial-line
// buffer.
type Tailer struct {
	cfg Config

	// In-memory offset — never persisted (anti-pattern #12).
	offset int64

	// Derived state, advanced inside the commit transaction.
	state state.State

	// Partial-line buffer: when a read ends mid-line we hold the
	// suffix until the next read so JSON parsing always sees
	// complete lines.
	partial []byte

	cancel context.CancelFunc
	done   chan struct{}
}

// New constructs a Tailer but does not start it. Call Run.
func New(cfg Config) *Tailer {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	if cfg.Notifier == nil {
		cfg.Notifier = noopNotifier{}
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stderr, "tailer: ", log.LstdFlags|log.Lmsgprefix)
	}
	return &Tailer{
		cfg:   cfg,
		state: state.Initial(),
		done:  make(chan struct{}),
	}
}

// Run blocks until ctx is cancelled. Owns its own goroutine. Resets
// the events-projection for this session at startup, then reads the
// event log from byte 0, applying derivation and committing on every
// batch.
func (t *Tailer) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	defer close(t.done)

	// 1. Wipe the events projection — startup guarantees a clean replay.
	if err := t.cfg.Store.Queries().DeleteEventsForSessionByID(ctx, t.cfg.SessionID); err != nil {
		return fmt.Errorf("wipe events projection: %w", err)
	}

	useFsnotify := true
	if t.cfg.UseFsnotify != nil {
		useFsnotify = *t.cfg.UseFsnotify
	}

	// 2. Set up file watcher (parent dir, since the file may not exist
	//    at startup).
	var watcher *fsnotify.Watcher
	if useFsnotify {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			t.cfg.Logger.Printf("fsnotify unavailable, falling back to polling: %v", err)
		} else {
			watcher = w
			defer watcher.Close()
			if dir := filepath.Dir(t.cfg.EventsPath); dir != "" {
				_ = os.MkdirAll(dir, 0o755)
				_ = watcher.Add(dir)
			}
		}
	}

	// 3. Initial drain — log may already have content.
	t.drain(ctx)

	ticker := time.NewTicker(t.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			t.drain(ctx)
		case ev, ok := <-watcherEvents(watcher):
			if !ok {
				watcher = nil
				continue
			}
			if ev.Name == t.cfg.EventsPath {
				t.drain(ctx)
			}
		case err := <-watcherErrors(watcher):
			if err != nil {
				t.cfg.Logger.Printf("fsnotify error for %s: %v", t.cfg.SessionID, err)
			}
		}
	}
}

// Stop cancels the run loop and waits for it to exit. Idempotent.
func (t *Tailer) Stop() {
	if t.cancel != nil {
		t.cancel()
	}
	<-t.done
}

// State returns a copy of the tailer's current derived state. Used
// by tests; not safe to call concurrently with Run.
func (t *Tailer) State() state.State { return t.state }

// drain reads new bytes from the event log and folds them into state.
// One commit transaction per drain — keeps each batch small enough
// that a partial failure leaves the offset un-advanced.
func (t *Tailer) drain(ctx context.Context) {
	if err := t.drainOnce(ctx); err != nil {
		t.cfg.Logger.Printf("drain %s: %v", t.cfg.EventsPath, err)
	}
}

func (t *Tailer) drainOnce(ctx context.Context) error {
	if t.cfg.EventsPath == "" {
		return nil
	}
	f, err := os.Open(t.cfg.EventsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // not created yet — not an error
		}
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	// Defensive: if the file shrank below our offset, replay from 0.
	if info.Size() < t.offset {
		t.cfg.Logger.Printf("file %s shrank (size=%d, offset=%d) — resetting to 0", t.cfg.EventsPath, info.Size(), t.offset)
		t.offset = 0
		t.partial = nil
		t.state = state.Initial()
		if err := t.cfg.Store.Queries().DeleteEventsForSessionByID(ctx, t.cfg.SessionID); err != nil {
			return fmt.Errorf("re-wipe events on shrink: %w", err)
		}
	}
	if info.Size() == t.offset {
		return nil
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return err
	}

	chunk, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if len(chunk) == 0 {
		return nil
	}

	combined := chunk
	if len(t.partial) > 0 {
		combined = append(append([]byte{}, t.partial...), chunk...)
	}

	var lines [][]byte
	scanner := bufio.NewScanner(newByteReader(combined))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		ln := append([]byte{}, scanner.Bytes()...)
		lines = append(lines, ln)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	hasTrailingNewline := len(combined) > 0 && combined[len(combined)-1] == '\n'
	var newPartial []byte
	if !hasTrailingNewline && len(lines) > 0 {
		newPartial = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
	}

	// Fold each line into derivation, queueing projected events.
	var batch []pendingEvent
	prev := t.state
	for _, ln := range lines {
		if len(ln) == 0 {
			continue
		}
		var ev ingest.Event
		if err := json.Unmarshal(ln, &ev); err != nil {
			t.cfg.Logger.Printf("session %s: skip malformed event line: %v", t.cfg.SessionID, err)
			continue
		}
		if ev.Version > ingest.SchemaVersion {
			t.cfg.Logger.Printf("session %s: skip future-version event v=%d (we know v=%d); upgrade gru", t.cfg.SessionID, ev.Version, ingest.SchemaVersion)
			continue
		}
		next, projs := state.Derive(prev, ev)
		if next.Status != prev.Status {
			t.cfg.Logger.Printf("session %s: status %s -> %s", t.cfg.SessionID, prev.Status, next.Status)
		}
		for _, p := range projs {
			batch = append(batch, pendingEvent{
				evtType:   p.Type,
				timestamp: p.Timestamp,
				payload:   p.Payload,
			})
		}
		prev = next
	}

	if err := t.commit(ctx, batch, prev); err != nil {
		return err
	}

	t.offset = info.Size()
	t.partial = newPartial
	if len(batch) > 0 {
		t.cfg.Notifier.Notify(t.cfg.SessionID)
	}
	t.state = prev
	return nil
}

// pendingEvent is one projected row queued during a drain pass.
type pendingEvent struct {
	evtType   string
	timestamp string
	payload   []byte
}

// commit writes the batch + the updated session row in one SQLite
// transaction. Either everything lands or nothing does.
func (t *Tailer) commit(ctx context.Context, batch []pendingEvent, st state.State) error {
	if len(batch) == 0 {
		// Nothing projected; still push the derived row in case
		// LastEventAt advanced.
		return t.cfg.Store.Queries().UpdateSessionDerived(ctx, store.UpdateSessionDerivedParams{
			Status:           string(st.Status),
			AttentionScore:   1.0, // attention engine writes its own column; this is the default for a freshly-initialized row
			LastEventAt:      ptrIfNonEmpty(st.LastEventAt),
			ClaudeStopReason: st.StopReason,
			PermissionMode:   "",
			ID:               t.cfg.SessionID,
		})
	}

	tx, err := t.cfg.Store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	for _, p := range batch {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO events (id, session_id, project_id, runtime, type, timestamp, payload)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(),
			t.cfg.SessionID,
			t.cfg.ProjectID,
			t.cfg.Runtime,
			p.evtType,
			p.timestamp,
			string(p.payload),
		); err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions
		 SET status = ?, last_event_at = ?, claude_stop_reason = ?
		 WHERE id = ?`,
		string(st.Status), ptrIfNonEmpty(st.LastEventAt), st.StopReason, t.cfg.SessionID,
	); err != nil {
		return fmt.Errorf("update session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

func ptrIfNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// byteReader wraps a []byte as an io.Reader.
type byteReader struct {
	b   []byte
	pos int
}

func newByteReader(b []byte) *byteReader { return &byteReader{b: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

func watcherEvents(w *fsnotify.Watcher) <-chan fsnotify.Event {
	if w == nil {
		return nil
	}
	return w.Events
}

func watcherErrors(w *fsnotify.Watcher) <-chan error {
	if w == nil {
		return nil
	}
	return w.Errors
}
