package tailer

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	for _, r := range rows {
		// Resolve transcript_path: prefer the row's stored value, else
		// derive it from the project's primary workdir + the row id.
		tp := r.TranscriptPath
		if tp == "" {
			tp = m.deriveTranscriptPath(ctx, r.ID, r.ProjectID)
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
	t, ok := m.tailers[sessionID]
	if ok {
		delete(m.tailers, sessionID)
	}
	m.mu.Unlock()
	if ok {
		t.Stop()
	}
}

// StopAll halts every running tailer. Used at server shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	all := make([]*Tailer, 0, len(m.tailers))
	for _, t := range m.tailers {
		all = append(all, t)
	}
	m.tailers = map[string]*Tailer{}
	m.mu.Unlock()
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
	t := New(Config{
		SessionID:      a.SessionID,
		ProjectID:      a.ProjectID,
		Runtime:        a.Runtime,
		TranscriptPath: a.TranscriptPath,
		NotifyPath:     a.NotifyPath,
		SupervisorPath: m.supervisorPath(a.SessionID),
		Store:          m.store,
		Notifier:       m.notifier,
	})
	m.tailers[a.SessionID] = t
	m.mu.Unlock()

	go func() {
		if err := t.Run(ctx); err != nil {
			m.logger.Printf("session %s tailer exited: %v", a.SessionID, err)
		}
		// Self-deregister on natural exit so a future AddSession with
		// the same ID can re-spawn cleanly.
		m.mu.Lock()
		if existing, ok := m.tailers[a.SessionID]; ok && existing == t {
			delete(m.tailers, a.SessionID)
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

// supervisorPath returns the per-session supervisor-event file path.
// The supervisor's FileEmitter appends here when a tmux pane goes
// away; the tailer reads it as a third input source.
func (m *Manager) supervisorPath(sessionID string) string {
	return filepath.Join(m.homeDir, ".gru", "supervisor", sessionID+".jsonl")
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
	if entries, err := os.ReadDir(dir); err == nil {
		// First pass: prefer a file whose name contains the gru
		// session id (unlikely but cheap to try).
		short := strings.ReplaceAll(sessionID, "-", "")[:8]
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			if strings.Contains(e.Name(), short) {
				return filepath.Join(dir, e.Name())
			}
		}
		// Otherwise, return the most recently modified .jsonl — this
		// is the heuristic the spec accepts (open question: see §3.10).
		var best string
		var bestMod int64
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Unix() > bestMod {
				best = filepath.Join(dir, e.Name())
				bestMod = info.ModTime().Unix()
			}
		}
		if best != "" {
			return best
		}
	}
	// Speculative path that doesn't exist yet — the tailer's watcher
	// on the directory will pick up whatever Claude creates.
	return filepath.Join(dir, sessionID+".jsonl")
}

// encodeCwd reproduces Claude Code's project-hash convention:
// "/Users/foo/x" → "-Users-foo-x". Empirical: every entry in
// ~/.claude/projects/ on the dev machine matches this transform.
// If Anthropic changes the scheme, this is the one place to fix.
func encodeCwd(cwd string) string {
	return strings.ReplaceAll(cwd, "/", "-")
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
