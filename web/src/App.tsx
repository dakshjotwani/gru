import { useEffect, useState } from 'react';
import { AttentionQueue } from './components/AttentionQueue';
import { LaunchModal } from './components/LaunchModal';
import { useSessionStream } from './hooks/useSessionStream';
import { useProjects } from './hooks/useProjects';
import { SessionStatus } from './types';
import styles from './App.module.css';

export function App() {
  const { projects, refetch: refetchProjects } = useProjects();
  const { sessions, events, connected } = useSessionStream(undefined, projects);
  const [showLaunch, setShowLaunch] = useState(false);

  // Global keyboard shortcut: press 'n' to open launch dialog.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'n' && !e.metaKey && !e.ctrlKey && !e.altKey) {
        const tag = (e.target as HTMLElement).tagName;
        if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
        setShowLaunch(true);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  // Register the service worker.
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
            className={[styles.dot, connected ? styles.dotConnected : styles.dotDisconnected].join(
              ' '
            )}
            title={connected ? 'Connected' : 'Disconnected'}
          />
          <span className={styles.sessionCount}>
            {activeCount} active session{activeCount !== 1 ? 's' : ''}
          </span>
          <button
            className={styles.launchBtn}
            onClick={() => setShowLaunch(true)}
            title="Launch a new agent session"
          >
            Launch
          </button>
        </div>
      </header>

      <main className={styles.main}>
        <AttentionQueue
          sessions={sessions}
          events={events}
          projects={projects}
          connected={connected}
        />
      </main>

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
