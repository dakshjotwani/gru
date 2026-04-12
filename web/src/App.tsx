import { useEffect } from 'react';
import { SessionGrid } from './components/SessionGrid';
import { useSessionStream } from './hooks/useSessionStream';
import { useProjects } from './hooks/useProjects';
import styles from './App.module.css';

export function App() {
  const { projects, loading: projectsLoading } = useProjects();
  const { sessions, events, connected, sessionsSortedByAttention } = useSessionStream();

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
      s.status !== 3 && // SESSION_STATUS_COMPLETED
      s.status !== 6 && // SESSION_STATUS_ERRORED
      s.status !== 7    // SESSION_STATUS_KILLED
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
        </div>
      </header>

      <main className={styles.main}>
        <SessionGrid
          projects={projects}
          sessions={sessions}
          events={events}
          sessionsSortedByAttention={sessionsSortedByAttention}
          loading={projectsLoading}
          connected={connected}
        />
      </main>
    </div>
  );
}
