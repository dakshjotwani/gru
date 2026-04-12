import type { Project, Session, SessionEvent } from '../types';
import { ProjectGroup } from './ProjectGroup';
import styles from './SessionGrid.module.css';

interface SessionGridProps {
  projects: Project[];
  sessions: Map<string, Session>;
  events: Map<string, SessionEvent[]>;
  sessionsSortedByAttention: (projectId: string) => Session[];
  loading: boolean;
  connected: boolean;
}

export function SessionGrid({
  projects,
  sessions,
  events,
  sessionsSortedByAttention,
  loading,
  connected,
}: SessionGridProps) {
  if (loading) {
    return (
      <div className={styles.state}>
        <p className={styles.loadingText}>Loading projects…</p>
      </div>
    );
  }

  if (projects.length === 0) {
    return (
      <div className={styles.state}>
        <p className={styles.emptyText}>No projects found. Start a session to see it here.</p>
      </div>
    );
  }

  const projectsWithSessions = projects.filter(
    (p) => sessionsSortedByAttention(p.id).length > 0
  );

  const noSessions = sessions.size === 0;

  return (
    <div className={styles.grid}>
      {!connected && (
        <div className={styles.banner} role="alert">
          Reconnecting to server…
        </div>
      )}

      {noSessions && connected && (
        <div className={styles.state}>
          <p className={styles.emptyText}>
            No active sessions. Launch an agent to get started.
          </p>
        </div>
      )}

      {projectsWithSessions.map((project) => (
        <ProjectGroup
          key={project.id}
          project={project}
          sessions={sessionsSortedByAttention(project.id)}
          events={events}
        />
      ))}
    </div>
  );
}
