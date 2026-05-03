package tailer

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dakshjotwani/gru/internal/env/spec"
	"github.com/dakshjotwani/gru/internal/store"
)

// Manager owns the set of running tailer goroutines and is the single
// place sessions get added to or removed from the live set. It does
// NOT own the publisher — Manager is the producer side and signals an
// injected Notifier on each commit.
type Manager struct {
	store    *store.Store
	notifier Notifier
	logger   *log.Logger

	// homeDir is the user's home directory; the manager resolves
	// transcript and notify paths under it. Set explicitly (rather
	// than calling os.UserHomeDir per session) so tests can point at
	// a tmpdir.
	homeDir string

	mu      sync.Mutex
	tailers map[string]*Tailer
	// cancels[sessionID] cancels the per-session run-ctx. Used by
	// RemoveSession / StopAll to break out of the restart-with-backoff
	// loop in spawn() — Tailer.Stop alone only ends one Run iteration,
	// the loop would just respawn.
	cancels map[string]context.CancelFunc
}

// NewManager constructs a Manager. notifier is signalled after every
// successful commit; pass a noop notifier in tests where you don't
// want to wire the publisher.
func NewManager(s *store.Store, notifier Notifier, homeDir string) *Manager {
	if notifier == nil {
		notifier = noopNotifier{}
	}
	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}
	return &Manager{
		store:    s,
		notifier: notifier,
		homeDir:  homeDir,
		logger:   log.New(os.Stderr, "tailer-mgr: ", log.LstdFlags|log.Lmsgprefix),
		tailers:  make(map[string]*Tailer),
		cancels:  make(map[string]context.CancelFunc),
	}
}

// Start spawns a tailer for each non-terminal session in the store.
// Idempotent — calling twice does not double-spawn. Should be called
// once on server startup, after the store is open and migrated.
func (m *Manager) Start(ctx context.Context) error {
	rows, err := m.store.Queries().ListNonTerminalSessions(ctx)
	if err != nil {
		return err
	}
	// Detect transcript_path collisions: if multiple sessions claim the
	// same file, all but one are stale (the heuristic that wrote them
	// was wrong). We can't tell which is right, so clear them all and
	// let notify-driven discovery sort it out per session.
	tpCount := make(map[string]int, len(rows))
	for _, r := range rows {
		if r.TranscriptPath != "" {
			tpCount[r.TranscriptPath]++
		}
	}
	for _, r := range rows {
		// Resolve transcript_path: prefer the row's stored value, else
		// derive it from the project's primary workdir + the row id.
		// Self-heal: ignore stored paths that don't exist on disk —
		// historically a misconfiguration could persist a bogus path
		// (e.g. ~/.gru/.claude/projects/... from a homeDir mixup), and
		// without this check the tailer would tail a nonexistent file
		// forever and the session's status would stay stuck.
		tp := r.TranscriptPath
		if tp != "" && tpCount[tp] > 1 {
			m.logger.Printf("session %s: stored transcript_path %q is shared by %d sessions; clearing for self-heal", r.ID, tp, tpCount[tp])
			tp = ""
		}
		if tp != "" {
			if _, err := os.Stat(tp); err != nil {
				m.logger.Printf("session %s: stored transcript_path %q does not exist; re-deriving", r.ID, tp)
				tp = ""
			}
		}
		// Prefer the deterministic <sid>.jsonl exact match when it
		// exists, even if a different (still-on-disk) path is
		// persisted. A persisted-but-wrong path can happen when a
		// sibling Claude process polluted the notify file in a prior
		// build (before the Notification-only guard) and the tailer
		// swapped onto the sibling's transcript. The exact-match file
		// only exists when launch-time --session-id worked, so this
		// self-heal is narrow: --resume'd sessions (whose <sid>.jsonl
		// won't exist on disk) keep their stored path.
		if exact := m.exactTranscriptPath(ctx, r.ID, r.ProjectID); exact != "" && exact != tp {
			if tp != "" {
				m.logger.Printf("session %s: preferring exact-match transcript %q over stored %q", r.ID, exact, tp)
			}
			tp = exact
		}
		if tp == "" {
			tp = m.deriveTranscriptPath(ctx, r.ID, r.ProjectID)
		}
		// Last-resort fallback for sessions started before --session-id
		// was added (no exact filename match) AND that haven't fired
		// any hook yet (no notify-driven discovery): match by file
		// birth time within a few seconds of the session's started_at.
		// Claude creates the .jsonl right when the process boots, so
		// the windows align tightly. We require an UNAMBIGUOUS match
		// (exactly one file in the window) to avoid silently picking
		// the wrong transcript.
		if tp == "" {
			tp = m.matchTranscriptByBirth(ctx, r)
			if tp != "" {
				m.logger.Printf("session %s: matched transcript_path by birth time: %s", r.ID, tp)
			}
		}
		np := m.notifyPath(r.ID)
		m.spawn(ctx, spawnArgs{
			SessionID:      r.ID,
			ProjectID:      r.ProjectID,
			Runtime:        r.Runtime,
			TranscriptPath: tp,
			NotifyPath:     np,
		})
	}
	return nil
}

// AddSession spawns a tailer for a freshly-launched session. Safe to
// call from the LaunchSession code path.
func (m *Manager) AddSession(ctx context.Context, sessionID, projectID, runtime, transcriptPath string) {
	if transcriptPath == "" {
		transcriptPath = m.deriveTranscriptPath(ctx, sessionID, projectID)
	}
	m.spawn(ctx, spawnArgs{
		SessionID:      sessionID,
		ProjectID:      projectID,
		Runtime:        runtime,
		TranscriptPath: transcriptPath,
		NotifyPath:     m.notifyPath(sessionID),
	})
}

// RemoveSession stops a session's tailer. Idempotent.
func (m *Manager) RemoveSession(sessionID string) {
	m.mu.Lock()
	cancel, hasCancel := m.cancels[sessionID]
	delete(m.cancels, sessionID)
	t, hasTailer := m.tailers[sessionID]
	delete(m.tailers, sessionID)
	m.mu.Unlock()
	if hasCancel {
		cancel() // breaks the restart loop
	}
	if hasTailer {
		t.Stop() // ends the current Run iteration
	}
}

// StopAll halts every running tailer. Used at server shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.cancels))
	for _, c := range m.cancels {
		cancels = append(cancels, c)
	}
	m.cancels = map[string]context.CancelFunc{}
	all := make([]*Tailer, 0, len(m.tailers))
	for _, t := range m.tailers {
		all = append(all, t)
	}
	m.tailers = map[string]*Tailer{}
	m.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	for _, t := range all {
		t.Stop()
	}
}

// Active returns the IDs of every session with a live tailer. Used by
// tests; caller should not modify the returned slice.
func (m *Manager) Active() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.tailers))
	for id := range m.tailers {
		out = append(out, id)
	}
	return out
}

// ── internals ────────────────────────────────────────────────────────

type spawnArgs struct {
	SessionID      string
	ProjectID      string
	Runtime        string
	TranscriptPath string
	NotifyPath     string
}

func (m *Manager) spawn(ctx context.Context, a spawnArgs) {
	m.mu.Lock()
	if _, ok := m.tailers[a.SessionID]; ok {
		m.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.cancels[a.SessionID] = cancel
	mkTailer := func() *Tailer {
		return New(Config{
			SessionID:      a.SessionID,
			ProjectID:      a.ProjectID,
			Runtime:        a.Runtime,
			TranscriptPath: a.TranscriptPath,
			NotifyPath:     a.NotifyPath,
			Store:          m.store,
			Notifier:       m.notifier,
		})
	}
	t := mkTailer()
	m.tailers[a.SessionID] = t
	m.mu.Unlock()

	m.logger.Printf("spawned tailer for session %s (transcript=%q notify=%q)", a.SessionID, a.TranscriptPath, a.NotifyPath)

	go func() {
		// Restart-with-backoff: a panicking/erroring Run shouldn't
		// silently kill a session's pipeline. The per-session runCtx is
		// cancelled by RemoveSession/StopAll, which breaks the loop;
		// otherwise we keep respawning so the dashboard never goes
		// permanently dark on a single session.
		const minBackoff = 1 * time.Second
		const maxBackoff = 30 * time.Second
		backoff := minBackoff
		current := t
		for {
			startedAt := time.Now()
			err := current.Run(runCtx)
			if runCtx.Err() != nil {
				if err != nil && !errors.Is(err, context.Canceled) {
					m.logger.Printf("session %s tailer exited on shutdown: %v", a.SessionID, err)
				}
				break
			}
			if err != nil {
				m.logger.Printf("session %s tailer exited unexpectedly: %v (will restart in %s)", a.SessionID, err, backoff)
			} else {
				m.logger.Printf("session %s tailer returned nil while ctx alive (will restart in %s)", a.SessionID, backoff)
			}
			// If Run survived for a while, reset backoff — we're not
			// in a tight crash loop.
			if time.Since(startedAt) > 60*time.Second {
				backoff = minBackoff
			}
			select {
			case <-runCtx.Done():
				break
			case <-time.After(backoff):
			}
			if runCtx.Err() != nil {
				break
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			current = mkTailer()
			m.mu.Lock()
			// If RemoveSession ran between iterations, the slot is gone
			// or holds someone else; bail rather than reinstate.
			if existing, ok := m.tailers[a.SessionID]; !ok || existing != t {
				m.mu.Unlock()
				return
			}
			m.tailers[a.SessionID] = current
			t = current
			m.mu.Unlock()
		}
		// Final cleanup on real exit.
		m.mu.Lock()
		if existing, ok := m.tailers[a.SessionID]; ok && existing == current {
			delete(m.tailers, a.SessionID)
		}
		if c, ok := m.cancels[a.SessionID]; ok {
			c()
			delete(m.cancels, a.SessionID)
		}
		m.mu.Unlock()
	}()
}

// notifyPath returns the per-session notify-file path. The Notification
// hook script appends here (see hooks/claude-notify.sh) and the tailer
// reads it as a second input source. Both ends agree on this layout —
// there is no separate config knob.
func (m *Manager) notifyPath(sessionID string) string {
	return filepath.Join(m.homeDir, ".gru", "notify", sessionID+".jsonl")
}

// DispatchSupervisorEvent routes a synthetic supervisor event to the
// matching session's tailer. Pass this method value as the
// supervisor.EventSink — the supervisor goroutine doesn't need to
// know how routing happens, just that it has a callback. Returns nil
// (and silently drops) when the session has no live tailer; the
// supervisor's own retry logic clears its already-emitted flag on
// error, and missing-tailer is *not* something we want it to retry.
func (m *Manager) DispatchSupervisorEvent(sessionID string, payload []byte) error {
	m.mu.Lock()
	t, ok := m.tailers[sessionID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	t.HandleSupervisorEvent(payload)
	return nil
}

// exactTranscriptPath returns the path of <sessionID>.jsonl under the
// project's primary workdir's claude-projects dir, or "" if it doesn't
// exist. Used by Start to prefer the deterministic --session-id-pinned
// transcript over a possibly-stale stored path.
func (m *Manager) exactTranscriptPath(ctx context.Context, sessionID, projectID string) string {
	cwd := m.projectPrimaryWorkdir(ctx, projectID)
	if cwd == "" {
		return ""
	}
	dir := filepath.Join(m.homeDir, ".claude", "projects", encodeCwd(cwd))
	exact := filepath.Join(dir, sessionID+".jsonl")
	if _, err := os.Stat(exact); err != nil {
		return ""
	}
	return exact
}

// deriveTranscriptPath maps a session to its Claude Code transcript
// file location. The "hash" Claude uses for ~/.claude/projects/ is the
// session's cwd with '/' replaced by '-' and a leading '-'. The session
// row's project_id is the absolute spec path; the spec's primary
// workdir is the cwd Claude was launched in.
//
// On miss (project not found, no .jsonl yet) we return a best-effort
// path that may not exist yet — fsnotify watches the parent directory
// so the file will be picked up the moment it appears.
func (m *Manager) deriveTranscriptPath(ctx context.Context, sessionID, projectID string) string {
	cwd := m.projectPrimaryWorkdir(ctx, projectID)
	if cwd == "" {
		return ""
	}
	dir := filepath.Join(m.homeDir, ".claude", "projects", encodeCwd(cwd))
	// The transcript is named <session-id>.jsonl. But Claude uses its
	// own session id, not Gru's — so we glob for any .jsonl whose
	// modification time is after our session's start. As a fallback,
	// we just construct the gru-id path; the file probably won't be
	// found, and the tailer will recover when Claude actually creates
	// its file (the watcher catches the create on the parent dir).
	// Look for an exact filename match: with the controller now passing
	// --session-id <gru-id>, Claude writes <gru-id>.jsonl deterministically.
	exact := filepath.Join(dir, sessionID+".jsonl")
	if _, err := os.Stat(exact); err == nil {
		return exact
	}
	// No exact match. Return empty rather than guess the most-recently
	// modified .jsonl: when multiple gru sessions share a project dir
	// (e.g. the gru repo's own minions plus the gru-state-report
	// session), every one of them would resolve to the same file and
	// pollute each other's status. The tailer's notify-driven discovery
	// (state.NotificationTranscriptPath, applied in Tailer.drainOne)
	// will set the right path the first time any hook fires for the
	// session — UserPromptSubmit, PostToolUse, Notification, etc.
	return ""
}

// encodeCwd reproduces Claude Code's project-hash convention:
// "/Users/foo/x" → "-Users-foo-x", and "." → "-" (dots in path
// components produce double-dashes, e.g. "/Users/foo/.gru/journal"
// → "-Users-foo--gru-journal"). Empirical: entries in
// ~/.claude/projects/ on the dev machine match this transform.
// If Anthropic changes the scheme, this is the one place to fix.
func encodeCwd(cwd string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
}

// matchTranscriptByBirth searches the project's ~/.claude/projects/
// dir for a single .jsonl whose file-birth time falls within
// [started_at - 1s, started_at + 10s]. Returns the path if exactly
// one matches; otherwise "". This is a fallback for sessions started
// before --session-id was wired through, used when filename-match
// fails AND no hook has fired yet to discover the path via notify.
func (m *Manager) matchTranscriptByBirth(ctx context.Context, r store.Session) string {
	if r.StartedAt == "" {
		return ""
	}
	started, err := time.Parse(time.RFC3339, r.StartedAt)
	if err != nil {
		return ""
	}
	cwd := m.projectPrimaryWorkdir(ctx, r.ProjectID)
	if cwd == "" {
		return ""
	}
	dir := filepath.Join(m.homeDir, ".claude", "projects", encodeCwd(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	lo := started.Add(-1 * time.Second)
	hi := started.Add(10 * time.Second)
	var matches []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		bt := fileBirthTime(full)
		if bt.IsZero() {
			continue
		}
		if bt.After(lo) && bt.Before(hi) {
			matches = append(matches, full)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

// fileBirthTime returns the macOS/BSD birth time for a file (the
// equivalent of "creation time"). On Linux this returns the zero
// time — Linux doesn't expose btime through the standard syscall
// stat struct, and the birth-time fallback is only used for the
// already-running-on-the-Mac-mini case so that's acceptable.
func fileBirthTime(path string) time.Time {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return time.Time{}
	}
	return birthTimeFromStat(&st)
}

// projectPrimaryWorkdir loads the spec at project.id and returns its
// first workdir — that's the cwd Claude Code was launched in, which is
// what Claude hashes into its ~/.claude/projects/ directory name.
// On any error we return "" — the caller treats that as "transcript
// path unresolvable, watch the dir and retry on create."
func (m *Manager) projectPrimaryWorkdir(ctx context.Context, projectID string) string {
	if projectID == "" {
		return ""
	}
	row, err := m.store.Queries().GetProject(ctx, projectID)
	if err != nil {
		return ""
	}
	loaded, err := spec.LoadFile(row.ID)
	if err != nil || len(loaded.Workdirs) == 0 {
		return ""
	}
	return loaded.Workdirs[0]
}
