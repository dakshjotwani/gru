package tailer

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"github.com/dakshjotwani/gru/internal/ingest"
	"github.com/dakshjotwani/gru/internal/store"
)

// Manager owns the set of running tailer goroutines and is the single
// place sessions get added to or removed from the live set. Producer
// side; signals an injected Notifier on each commit.
type Manager struct {
	store    *store.Store
	notifier Notifier
	logger   *log.Logger

	homeDir string

	mu      sync.Mutex
	tailers map[string]*Tailer
	cancels map[string]context.CancelFunc
}

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
// Idempotent.
func (m *Manager) Start(ctx context.Context) error {
	rows, err := m.store.Queries().ListNonTerminalSessions(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		m.spawn(ctx, spawnArgs{
			SessionID: r.ID,
			ProjectID: r.ProjectID,
			Runtime:   r.Runtime,
		})
	}
	return nil
}

// AddSession spawns a tailer for a freshly-launched session.
func (m *Manager) AddSession(ctx context.Context, sessionID, projectID, runtime string) {
	m.spawn(ctx, spawnArgs{
		SessionID: sessionID,
		ProjectID: projectID,
		Runtime:   runtime,
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
		cancel()
	}
	if hasTailer {
		t.Stop()
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

// Active returns the IDs of every session with a live tailer.
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
	SessionID string
	ProjectID string
	Runtime   string
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
			SessionID:  a.SessionID,
			ProjectID:  a.ProjectID,
			Runtime:    a.Runtime,
			EventsPath: ingest.LogPath(m.homeDir, a.SessionID),
			Store:      m.store,
			Notifier:   m.notifier,
		})
	}
	t := mkTailer()
	m.tailers[a.SessionID] = t
	m.mu.Unlock()

	m.logger.Printf("spawned tailer for session %s (events=%q)", a.SessionID, ingest.LogPath(m.homeDir, a.SessionID))

	go func() {
		// Restart-with-backoff: a panicking/erroring Run shouldn't
		// silently kill a session's pipeline. The per-session runCtx
		// is cancelled by RemoveSession/StopAll.
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
			if existing, ok := m.tailers[a.SessionID]; !ok || existing != t {
				m.mu.Unlock()
				return
			}
			m.tailers[a.SessionID] = current
			t = current
			m.mu.Unlock()
		}
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
