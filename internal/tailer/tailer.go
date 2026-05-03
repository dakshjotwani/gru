// Package tailer is the producer side of Gru's state pipeline rev 2:
// per-session goroutines that tail Claude Code's per-session JSONL
// transcript (and the residual permission-notification file), apply the
// state-derivation function, and write the resulting state to SQLite in
// a single transaction. See docs/superpowers/specs/2026-04-24-state-pipeline-design.md.
//
// Replay-from-zero is the central correctness property: on (re)start,
// each tailer wipes its session's events-projection cache and re-reads
// the transcript from byte 0. The byte offset never leaves RAM, so
// there is no failure mode where the offset advances and the state
// write fails silently (anti-pattern #12 in the spec).
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

	"github.com/dakshjotwani/gru/internal/state"
	"github.com/dakshjotwani/gru/internal/store"
	"github.com/dakshjotwani/gru/internal/store/db"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
)

// Notifier is the publisher-side hook the tailer signals after every
// commit. Kept as a single-method interface so the tailer doesn't
// import the publisher package (avoiding a cycle) and so tests can
// inject a counting fake.
type Notifier interface {
	// Notify is called once per commit, with the session ID. The
	// publisher uses this as a wake-up signal to scan the events table
	// for new rows.
	Notify(sessionID string)
}

// noopNotifier swallows wake-ups; used when the caller has not wired a
// publisher yet (e.g. unit tests).
type noopNotifier struct{}

func (noopNotifier) Notify(string) {}

// Config wires a Tailer to its inputs. All fields must be set except
// PollInterval and Logger, which have sensible defaults.
type Config struct {
	SessionID      string
	ProjectID      string
	Runtime        string
	TranscriptPath string // ~/.claude/projects/<hash>/<sid>.jsonl
	NotifyPath     string // ~/.gru/notify/<sid>.jsonl
	SupervisorPath string // ~/.gru/supervisor/<sid>.jsonl (synthetic events)

	Store    *store.Store
	Notifier Notifier

	// PollInterval is the fallback poll cadence used when fsnotify is
	// unavailable. Defaults to 250 ms. Tests set it shorter so they
	// don't have to sleep for real wall time.
	PollInterval time.Duration

	// UseFsnotify, when false, forces the tailer onto its polling code
	// path. Tests use this to verify the fallback works.
	UseFsnotify *bool

	Logger *log.Logger
}

// Tailer is one running session-tailer goroutine. Don't share across
// sessions — each instance owns its own offsets and file descriptors.
type Tailer struct {
	cfg Config

	// in-memory offsets — never persisted (anti-pattern #12).
	transcriptOffset int64
	notifyOffset     int64
	supervisorOffset int64

	// derived state, advanced inside the commit transaction.
	state state.State

	// partial-line buffers: when a read ends mid-line we hold the
	// suffix until the next read so JSON parsing always sees complete
	// lines.
	transcriptBuf []byte
	notifyBuf     []byte
	supervisorBuf []byte

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
	return &Tailer{cfg: cfg, state: state.Initial(), done: make(chan struct{})}
}

// Run blocks until ctx is cancelled. Owns its own goroutine. Resets
// the events-projection for this session at startup, then reads the
// transcript and notify files from byte 0, applying derivation and
// committing on every batch.
//
// The function returns nil on normal cancellation. Errors that abort
// the loop (DB closed, etc.) are returned so the caller can decide
// whether to restart.
func (t *Tailer) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	defer close(t.done)

	// 1. Wipe the events projection for this session — startup
	//    guarantees a clean replay (spec §3.2).
	if err := t.cfg.Store.Queries().DeleteEventsForSessionByID(ctx, t.cfg.SessionID); err != nil {
		return fmt.Errorf("wipe events projection: %w", err)
	}

	// 2. Pull the persisted session row so we start from the same
	//    initial state every restart. transcript_path may be set on
	//    the row already (from session launch) — we use it but the
	//    config wins.
	if t.cfg.TranscriptPath != "" {
		if err := t.cfg.Store.Queries().UpdateSessionTranscriptPath(ctx, db.UpdateSessionTranscriptPathParams{
			TranscriptPath: t.cfg.TranscriptPath,
			ID:             t.cfg.SessionID,
		}); err != nil {
			t.cfg.Logger.Printf("update transcript_path for %s: %v", t.cfg.SessionID, err)
		}
	}

	useFsnotify := true
	if t.cfg.UseFsnotify != nil {
		useFsnotify = *t.cfg.UseFsnotify
	}

	// 3. Set up file watchers.
	var watcher *fsnotify.Watcher
	if useFsnotify {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			t.cfg.Logger.Printf("fsnotify unavailable, falling back to polling: %v", err)
		} else {
			watcher = w
			defer watcher.Close()
		}
	}

	// 4. Initial drain — both files may already have content.
	t.drain(ctx)

	// 5. Watch loop.
	if watcher != nil {
		// Watch the parent directories so we catch the create event if
		// the transcript file doesn't exist yet at startup. fsnotify
		// can't watch a file that doesn't exist.
		if dir := filepath.Dir(t.cfg.TranscriptPath); dir != "" {
			_ = watcher.Add(dir)
		}
		if dir := filepath.Dir(t.cfg.NotifyPath); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
			_ = watcher.Add(dir)
		}
		if dir := filepath.Dir(t.cfg.SupervisorPath); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
			_ = watcher.Add(dir)
		}
	}

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
				// Watcher closed unexpectedly — fall back to poll-only.
				watcher = nil
				continue
			}
			// Filter to exactly our three files. We watch parent dirs
			// so fsnotify fires on Create (the file may not exist at
			// startup), but ev.Name carries the full path — a map
			// lookup is enough to skip sibling sessions' writes.
			watched := map[string]struct{}{
				t.cfg.TranscriptPath: {},
				t.cfg.NotifyPath:     {},
				t.cfg.SupervisorPath: {},
			}
			if _, ok := watched[ev.Name]; ok {
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

// State returns a copy of the tailer's current derived state. Used by
// tests; not safe to call concurrently with Run.
func (t *Tailer) State() state.State {
	return t.state
}

// drain reads any new bytes from all input files and folds them into
// state. One commit transaction per file per drain — keeps each batch
// small enough that a partial failure leaves the offset un-advanced.
//
// Order matters: notify is drained BEFORE the transcript so any
// transcript_path it carries can correct the path we read from THIS
// pass. Without this, a session whose stored path is wrong would
// continue to read the wrong file for an extra cycle every drain.
func (t *Tailer) drain(ctx context.Context) {
	if t.cfg.NotifyPath != "" {
		if err := t.drainOne(ctx, t.cfg.NotifyPath, &t.notifyOffset, &t.notifyBuf, state.SourceNotification); err != nil {
			t.cfg.Logger.Printf("drain notify %s: %v", t.cfg.NotifyPath, err)
		}
	}
	if err := t.drainOne(ctx, t.cfg.TranscriptPath, &t.transcriptOffset, &t.transcriptBuf, state.SourceTranscript); err != nil {
		t.cfg.Logger.Printf("drain transcript %s: %v", t.cfg.TranscriptPath, err)
	}
	if t.cfg.SupervisorPath != "" {
		if err := t.drainOne(ctx, t.cfg.SupervisorPath, &t.supervisorOffset, &t.supervisorBuf, state.SourceSupervisor); err != nil {
			t.cfg.Logger.Printf("drain supervisor %s: %v", t.cfg.SupervisorPath, err)
		}
	}
}

// drainOne reads from path starting at *offset, parses complete lines,
// folds them through Derive, and commits a single batch in one SQL
// transaction. Updates *offset and *partial only after a successful
// commit; on error the next drain re-reads the same bytes.
func (t *Tailer) drainOne(ctx context.Context, path string, offset *int64, partial *[]byte, src state.Source) error {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // file not created yet; not an error.
		}
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	// Defensive: if the file shrank below our offset, something
	// truncated it (Claude doesn't do this, but a misbehaving tool
	// might). Reset to 0 and replay from scratch — same as a fresh start.
	if info.Size() < *offset {
		t.cfg.Logger.Printf("file %s shrank (size=%d, offset=%d) — resetting to 0", path, info.Size(), *offset)
		*offset = 0
		*partial = nil
	}
	if info.Size() == *offset {
		return nil // nothing new
	}

	if _, err := f.Seek(*offset, io.SeekStart); err != nil {
		return err
	}

	// Read everything new in one shot. Transcripts cap out around
	// 10s of MB even for very long sessions; we have plenty of RAM
	// at this scale.
	chunk, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if len(chunk) == 0 {
		return nil
	}

	// Prepend any partial line from the previous drain.
	combined := chunk
	if len(*partial) > 0 {
		combined = append(append([]byte{}, *partial...), chunk...)
	}

	// Split on '\n'. The last segment may be a partial line if the
	// chunk didn't end in a newline; carry it forward.
	var lines [][]byte
	scanner := bufio.NewScanner(newByteReader(combined))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // up to 16 MB lines
	for scanner.Scan() {
		ln := append([]byte{}, scanner.Bytes()...) // copy; scanner reuses its buffer
		lines = append(lines, ln)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// Detect trailing partial line: if combined doesn't end with '\n'
	// and we have at least one line, the last "line" is partial.
	hasTrailingNewline := len(combined) > 0 && combined[len(combined)-1] == '\n'
	var newPartial []byte
	if !hasTrailingNewline && len(lines) > 0 {
		newPartial = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
	}

	// Fold into derivation, queueing projected events.
	var batch []pendingEvent
	var learnedTranscriptPath string
	prevState := t.state
	for _, ln := range lines {
		if len(ln) == 0 {
			continue
		}
		// Notify lines carry Claude's own transcript_path. Use the
		// last value seen in this batch as ground truth for the file
		// we should be tailing — this is the only reliable way to
		// reconcile a gru session with its Claude .jsonl when the
		// project dir contains transcripts from multiple sessions.
		if src == state.SourceNotification {
			if tp := state.NotificationTranscriptPath(ln); tp != "" {
				learnedTranscriptPath = tp
			}
		}
		next, p := state.Derive(prevState, src, ln)
		// Emit a synthetic session.transition row whenever the
		// derivation function flips Status. The frontend trusts
		// these events as the single source of truth for status
		// changes (see web/src/hooks/useSessionStream.ts) and never
		// re-derives. Without this, transcript-driven flips
		// (running ↔ idle on tool_use / end_turn) update the
		// sessions row in SQLite but never reach the UI, which
		// stays pinned to whatever status the last
		// notification/supervisor event set.
		// Skip if Derive already emitted its own transition (notification/
		// supervisor sources do — we'd double-publish otherwise).
		alreadyTransition := p != nil && p.Type == "session.transition"
		if next.Status != prevState.Status && !alreadyTransition {
			ts := ""
			if p != nil {
				ts = p.Timestamp
			}
			if ts == "" {
				ts = time.Now().UTC().Format(time.RFC3339)
			}
			tpayload, _ := json.Marshal(map[string]string{
				"from": string(prevState.Status),
				"to":   string(next.Status),
				"why":  "transcript:" + sourceName(src),
			})
			batch = append(batch, pendingEvent{evtType: "session.transition", timestamp: ts, payload: tpayload})
			t.cfg.Logger.Printf("session %s: status %s -> %s (via %s)", t.cfg.SessionID, prevState.Status, next.Status, sourceName(src))
		} else if next.Status != prevState.Status && alreadyTransition {
			// Notification/supervisor sources synthesize their own
			// transition projection inside Derive; log here so the
			// flip is still visible in server logs.
			t.cfg.Logger.Printf("session %s: status %s -> %s (via %s)", t.cfg.SessionID, prevState.Status, next.Status, sourceName(src))
		}
		prevState = next
		if p != nil {
			ts := p.Timestamp
			if ts == "" {
				ts = time.Now().UTC().Format(time.RFC3339)
			}
			payload := p.Payload
			if payload == nil {
				payload = ln
			}
			batch = append(batch, pendingEvent{evtType: p.Type, timestamp: ts, payload: payload})
		}
	}

	// Commit the batch + the new derived row in one transaction. If
	// any insert fails, the whole transaction rolls back and we leave
	// the offset where it was — next drain re-reads the same bytes.
	if err := t.commit(ctx, batch, prevState); err != nil {
		return err
	}

	// Only on commit success do we advance the in-memory offset.
	*offset = info.Size()
	*partial = newPartial

	// Wake the publisher. Cheap; the publisher debounces via its seq
	// scan loop.
	if len(batch) > 0 {
		t.cfg.Notifier.Notify(t.cfg.SessionID)
	}

	t.state = prevState

	// If we learned a new transcript path from notifications, swap
	// over for the next drain. Reset the offset so we replay the
	// real transcript from byte 0 — the events projection has
	// already been wiped at startup, and replay is idempotent.
	if learnedTranscriptPath != "" && learnedTranscriptPath != t.cfg.TranscriptPath {
		t.cfg.Logger.Printf("session %s: learned transcript_path from notify: %s (was %s)",
			t.cfg.SessionID, learnedTranscriptPath, t.cfg.TranscriptPath)
		t.cfg.TranscriptPath = learnedTranscriptPath
		t.transcriptOffset = 0
		t.transcriptBuf = nil
		// Persist so a later restart starts from the right path.
		if err := t.cfg.Store.Queries().UpdateSessionTranscriptPath(ctx, db.UpdateSessionTranscriptPathParams{
			TranscriptPath: learnedTranscriptPath,
			ID:             t.cfg.SessionID,
		}); err != nil {
			t.cfg.Logger.Printf("session %s: persist learned transcript_path: %v", t.cfg.SessionID, err)
		}
		// Wipe events projection so the replay-from-zero of the
		// new transcript starts from a clean slate. Without this we'd
		// leave the old transcript's events in place and append the
		// new ones on top, double-projecting state.
		if err := t.cfg.Store.Queries().DeleteEventsForSessionByID(ctx, t.cfg.SessionID); err != nil {
			t.cfg.Logger.Printf("session %s: wipe events projection on transcript swap: %v", t.cfg.SessionID, err)
		}
		// Reset derivation state too — we're starting over.
		t.state = state.Initial()
	}
	return nil
}

// pendingEvent is one projected row queued during a drain pass; the
// commit transaction inserts the whole batch atomically.
type pendingEvent struct {
	evtType   string
	timestamp string
	payload   []byte
}

// commit writes the batch of projected events plus the updated session
// row in a single SQLite transaction. Either everything lands or
// nothing does — the offset advance is gated on success.
func (t *Tailer) commit(ctx context.Context, batch []pendingEvent, st state.State) error {
	if len(batch) == 0 {
		// Status-affecting state may still have changed even with no
		// projection (e.g. a pure tool_result that resolved a pending
		// id without flipping status). Update the row anyway so the
		// derived columns track the fold.
		return t.cfg.Store.Queries().UpdateSessionDerived(ctx, store.UpdateSessionDerivedParams{
			Status:           string(st.Status),
			AttentionScore:   st.AttentionScore,
			LastEventAt:      ptrIfNonEmpty(st.LastEventAt),
			ClaudeStopReason: st.ClaudeStopReason,
			PermissionMode:   st.PermissionMode,
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

	lastEventAt := ptrIfNonEmpty(st.LastEventAt)
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions
		 SET status = ?, attention_score = ?, last_event_at = ?, claude_stop_reason = ?, permission_mode = ?
		 WHERE id = ?`,
		string(st.Status), st.AttentionScore, lastEventAt, st.ClaudeStopReason, st.PermissionMode, t.cfg.SessionID,
	); err != nil {
		return fmt.Errorf("update session: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	tx = nil
	return nil
}

// ptrIfNonEmpty returns nil for empty strings (so SQLite stores NULL)
// and a pointer otherwise. The sqlc-generated *string parameter type
// uses NULL for nil and a value for non-nil.
func ptrIfNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// byteReader wraps a []byte as an io.Reader. We use bufio.Scanner for
// '\n'-delimited splitting which expects an io.Reader, but we have a
// []byte already in memory — wrapping is cheaper than re-reading.
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

// watcherEvents returns the watcher's Events channel, or a nil channel
// (which blocks forever in select) if the watcher itself is nil. This
// keeps the run loop's select statement uniform across "fsnotify
// available" and "polling only" paths.
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

func sourceName(s state.Source) string {
	switch s {
	case state.SourceTranscript:
		return "transcript"
	case state.SourceNotification:
		return "notification"
	case state.SourceSupervisor:
		return "supervisor"
	default:
		return "unknown"
	}
}

