import { useEffect, useRef, useState } from 'react';
import { AttentionQueue } from './components/AttentionQueue';
import { ChatPanel } from './components/ChatPanel';
import { LaunchModal } from './components/LaunchModal';
import { PWAInstallBanner } from './components/PWAInstallBanner';
import { TerminalPanel } from './components/TerminalPanel';
import { useDeviceRegistration } from './hooks/useDeviceRegistration';
import { useSessionStream } from './hooks/useSessionStream';
import { useProjects } from './hooks/useProjects';
import { SessionStatus } from './types';
import styles from './App.module.css';

// Per-session view preference (terminal vs chat). Persisted to
// localStorage so flipping a session to chat on iPhone survives a
// reload. Default picks a sensible mode based on viewport: narrow
// screens prefer chat; wider (iPad landscape / desktop) prefer
// terminal. iPad portrait sits in between and matches whatever the
// user last chose globally.
type SessionView = 'terminal' | 'chat';

function defaultView(): SessionView {
  if (typeof window === 'undefined') return 'terminal';
  return window.matchMedia('(max-width: 600px)').matches ? 'chat' : 'terminal';
}

function readSessionView(id: string): SessionView {
  try {
    const raw = localStorage.getItem(`gru.view.${id}`);
    if (raw === 'chat' || raw === 'terminal') return raw;
  } catch {
    // ignore
  }
  return defaultView();
}

function writeSessionView(id: string, v: SessionView) {
  try {
    localStorage.setItem(`gru.view.${id}`, v);
  } catch {
    // ignore
  }
}

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

  // The Gru assistant is a singleton session with role="assistant". It owns
  // the main pane whenever no minion is selected — clicking a minion swaps
  // the pane to that session's terminal; deselecting (Esc / header / sidebar
  // button) swaps back to the assistant. Falls back to the old empty state
  // when the assistant session hasn't spawned yet (first-run or disabled).
  const assistantSession = Array.from(sessions.values()).find((s) => s.role === 'assistant') ?? null;
  const mainSession = selectedSession ?? assistantSession;

  const deselect = () => {
    setSelectedSessionId(null);
    setTimeout(() => focusTerminalRef.current?.(), 50);
  };

  // Ctrl+\ — toggle between sidebar nav mode and terminal.
  // Ctrl+N / Ctrl+P — navigate sessions while sidebar is focused.
  // Enter — confirm selection and return focus to terminal.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Ctrl+\ toggles sidebar nav mode from anywhere — always active.
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

      // Yield to the terminal when it has focus: xterm's custom key
      // handler claims Ctrl+C / Tab / Escape / arrows etc. in that case.
      const active = document.activeElement as HTMLElement | null;
      if (active?.closest('[data-gru-terminal]')) return;

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
        // Esc deselects the current minion and returns the main pane to Gru.
        // Guard against stealing Escape from inputs/modals — if focus is in a
        // form element or the Launch modal is open, let the host component
        // handle it (e.g. modal closes itself on Escape).
        if (e.key === 'Escape' && selectedSessionId && !showLaunch) {
          const tag = (e.target as HTMLElement).tagName;
          if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
          e.preventDefault();
          deselect();
        }
      }
    };

    // capture: true so we intercept before xterm sees the event.
    window.addEventListener('keydown', onKey, { capture: true });
    return () => window.removeEventListener('keydown', onKey, { capture: true });
  }, [sidebarFocused, selectedSessionId, showLaunch]);

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
      <PWAInstallBanner />
      <header className={styles.header}>
        <div className={styles.brand}>
          <button
            type="button"
            className={styles.titleButton}
            onClick={deselect}
            title="Return to Gru (Esc)"
            aria-label="Return to Gru assistant"
          >
            <h1 className={styles.title}>Gru</h1>
          </button>
        </div>
        <div className={styles.statusRow}>
          <span
            className={[styles.dot, connected ? styles.dotConnected : styles.dotDisconnected].join(' ')}
            title={connected ? 'Connected' : 'Disconnected'}
          />
          <span className={styles.sessionCount}>
            {activeCount} active minion{activeCount !== 1 ? 's' : ''}
          </span>
          <NotificationsButton />
          <button
            className={styles.launchBtn}
            onClick={() => setShowLaunch(true)}
            title="Launch a new minion (n)"
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
          <button
            type="button"
            className={[styles.askGruButton, selectedSessionId === null ? styles.askGruButtonActive : ''].filter(Boolean).join(' ')}
            onClick={deselect}
            title="Chat with Gru (Esc)"
          >
            <span className={styles.askGruIcon}>💬</span>
            <span className={styles.askGruLabel}>Ask Gru</span>
          </button>
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
          {mainSession ? (
            <SessionView
              session={mainSession}
              events={events.get(mainSession.id) ?? []}
              focusRef={focusTerminalRef}
            />
          ) : (
            <div className={styles.emptyTerminal}>
              <p className={styles.emptyTerminalText}>
                Gru is starting up…
              </p>
              <p className={styles.emptyTerminalHint}>
                If this persists, the assistant may be disabled (see <code>~/.gru/server.yaml</code>).
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

// NotificationsButton is the operator's always-visible control for
// Web Push registration. It shows the current state on its face
// (🔔 enable / 🔕 unsupported / ✓ registered) so there's never any
// ambiguity about why notifications aren't working. Tapping it
// triggers (or retries) the permission + subscribe flow.
function NotificationsButton() {
  const { permission, registered, requestSubscription, error } = useDeviceRegistration();
  let label: string;
  let title: string;
  if (permission === 'unsupported') {
    label = '🔕 n/a';
    title = 'Web Push not supported in this browser (iOS 16.4+ PWA required)';
  } else if (permission === 'denied') {
    label = '🔕 blocked';
    title = 'Notifications denied — re-enable in iOS Settings → Gru';
  } else if (registered && permission === 'granted') {
    label = '🔔 on';
    title = 'Registered. Tap to re-register on this device.';
  } else if (permission === 'granted') {
    label = '🔔 register';
    title = 'Notifications granted but this device isn\'t registered yet.';
  } else {
    label = '🔔 enable';
    title = 'Enable push notifications on this device';
  }
  if (error) title = `${title} (error: ${error})`;
  return (
    <button
      className={styles.launchBtn}
      onClick={() => requestSubscription()}
      title={title}
    >
      {label}
    </button>
  );
}

// SessionView picks between the terminal (full fidelity, iPad/desktop
// default) and the chat-style renderer (touch-friendly, iPhone default)
// for a single session. The chosen view is per-session and sticky in
// localStorage. The toggle lives as a small tab row above the panel.
interface SessionViewProps {
  session: import('./types').Session;
  events: import('./types').SessionEvent[];
  focusRef?: React.RefObject<(() => void) | null>;
}

function SessionView({ session, events, focusRef }: SessionViewProps) {
  const [view, setView] = useState<SessionView>(() => readSessionView(session.id));

  // Persist + refocus terminal when flipping back to it.
  const flipTo = (v: SessionView) => {
    setView(v);
    writeSessionView(session.id, v);
    if (v === 'terminal') {
      setTimeout(() => focusRef?.current?.(), 60);
    }
  };

  return (
    <div className={styles.sessionViewShell}>
      <div className={styles.viewToggle}>
        <button
          type="button"
          className={view === 'terminal' ? styles.viewToggleActive : styles.viewToggleBtn}
          onClick={() => flipTo('terminal')}
        >
          Terminal
        </button>
        <button
          type="button"
          className={view === 'chat' ? styles.viewToggleActive : styles.viewToggleBtn}
          onClick={() => flipTo('chat')}
        >
          Chat
        </button>
      </div>
      {view === 'terminal' ? (
        <TerminalPanel key={session.id} session={session} focusRef={focusRef} />
      ) : (
        <ChatPanel session={session} events={events} />
      )}
    </div>
  );
}
