import { useEffect, useRef, useState } from 'react';
import { AttentionQueue } from './components/AttentionQueue';
import { LaunchModal } from './components/LaunchModal';
import { TerminalPanel } from './components/TerminalPanel';
import { useSessionStream } from './hooks/useSessionStream';
import { useProjects } from './hooks/useProjects';
import { SessionStatus } from './types';
import styles from './App.module.css';

export function App() {
  const { projects, refetch: refetchProjects } = useProjects();
  const { sessions, events, connected } = useSessionStream(undefined, projects);
  const [showLaunch, setShowLaunch] = useState(false);
  const [selectedSessionId, setSelectedSessionId] = useState<string | null>(null);
  const [sidebarFocused, setSidebarFocused] = useState(false);

  // AttentionQueue keeps this updated with the current visible+sorted session IDs.
  const sortedSessionIdsRef = useRef<string[]>([]);

  // TerminalPanel registers a focus() fn here so we can pull focus back to it.
  const focusTerminalRef = useRef<(() => void) | null>(null);

  const selectedSession = selectedSessionId ? sessions.get(selectedSessionId) ?? null : null;

  // Ctrl+\ — toggle between sidebar nav mode and terminal.
  // Ctrl+N / Ctrl+P — navigate sessions while sidebar is focused.
  // Enter — confirm selection and return focus to terminal.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Ctrl+\ toggles sidebar nav mode from anywhere.
      if (e.key === '\\' && e.ctrlKey && !e.altKey && !e.shiftKey) {
        e.preventDefault();
        e.stopPropagation();
        setSidebarFocused((prev) => {
          if (prev) {
            // Leaving sidebar → focus terminal.
            focusTerminalRef.current?.();
            return false;
          }
          return true;
        });
        return;
      }

      // Navigation and confirmation only active while sidebar is focused.
      if (sidebarFocused) {
        if ((e.key === 'n' && e.ctrlKey) || (e.key === 'p' && e.ctrlKey)) {
          e.preventDefault();
          e.stopPropagation();
          const ids = sortedSessionIdsRef.current;
          if (ids.length === 0) return;
          const currentIdx = selectedSessionId ? ids.indexOf(selectedSessionId) : -1;
          let nextIdx: number;
          if (e.key === 'n') {
            nextIdx = currentIdx < ids.length - 1 ? currentIdx + 1 : 0;
          } else {
            nextIdx = currentIdx > 0 ? currentIdx - 1 : ids.length - 1;
          }
          setSelectedSessionId(ids[nextIdx]);
          return;
        }

        if (e.key === 'Enter') {
          e.preventDefault();
          e.stopPropagation();
          setSidebarFocused(false);
          focusTerminalRef.current?.();
          return;
        }
      }

      // Non-capture shortcuts (sidebar not focused, no special modifier).
      if (!e.ctrlKey && !e.metaKey && !e.altKey) {
        if (e.key === 'n') {
          const tag = (e.target as HTMLElement).tagName;
          if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
          setShowLaunch(true);
        }
      }
    };

    // capture: true so we intercept before xterm sees the event.
    window.addEventListener('keydown', onKey, { capture: true });
    return () => window.removeEventListener('keydown', onKey, { capture: true });
  }, [sidebarFocused, selectedSessionId]);

  // Register service worker.
  useEffect(() => {
    if ('serviceWorker' in navigator) {
      navigator.serviceWorker
        .register('/sw.js')
        .catch((err) => console.warn('SW registration failed:', err));
    }
  }, []);

  const activeCount = Array.from(sessions.values()).filter(
    (s) =>
      s.status !== SessionStatus.COMPLETED &&
      s.status !== SessionStatus.ERRORED &&
      s.status !== SessionStatus.KILLED
  ).length;

  return (
    <div className={styles.app}>
      <header className={styles.header}>
        <div className={styles.brand}>
          <h1 className={styles.title}>Gru</h1>
          <span className={styles.subtitle}>Mission Control</span>
        </div>
        <div className={styles.statusRow}>
          <span
            className={[styles.dot, connected ? styles.dotConnected : styles.dotDisconnected].join(' ')}
            title={connected ? 'Connected' : 'Disconnected'}
          />
          <span className={styles.sessionCount}>
            {activeCount} active session{activeCount !== 1 ? 's' : ''}
          </span>
          <button
            className={styles.launchBtn}
            onClick={() => setShowLaunch(true)}
            title="Launch a new agent session (n)"
          >
            Launch
          </button>
        </div>
      </header>

      <div className={styles.workspace}>
        <aside className={[styles.sidebar, sidebarFocused ? styles.sidebarActive : ''].filter(Boolean).join(' ')}>
          {sidebarFocused && (
            <div className={styles.navHint}>
              <kbd>Ctrl+N</kbd><kbd>Ctrl+P</kbd> navigate &nbsp;·&nbsp; <kbd>Enter</kbd> or <kbd>Ctrl+\</kbd> back to terminal
            </div>
          )}
          <AttentionQueue
            sessions={sessions}
            events={events}
            projects={projects}
            connected={connected}
            onSessionSelect={(id) => {
              setSelectedSessionId(id);
              setSidebarFocused(false);
              // Small delay to let TerminalPanel mount before focusing.
              setTimeout(() => focusTerminalRef.current?.(), 50);
            }}
            selectedSessionId={selectedSessionId ?? undefined}
            onSortedSessions={(ids) => { sortedSessionIdsRef.current = ids; }}
          />
        </aside>

        <main className={styles.main}>
          {selectedSession ? (
            <TerminalPanel
              key={selectedSession.id}
              session={selectedSession}
              focusRef={focusTerminalRef}
            />
          ) : (
            <div className={styles.emptyTerminal}>
              <p className={styles.emptyTerminalText}>
                Select a session to open its terminal
              </p>
              <p className={styles.emptyTerminalHint}>
                <kbd>Ctrl+\</kbd> to navigate sessions
              </p>
            </div>
          )}
        </main>
      </div>

      {showLaunch && (
        <LaunchModal
          projects={projects}
          onClose={() => setShowLaunch(false)}
          onLaunched={() => refetchProjects()}
        />
      )}
    </div>
  );
}
