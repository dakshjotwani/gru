import { useState } from 'react';
import type { Project, Session, SessionEvent } from '../types';
import { SessionCard } from './SessionCard';
import styles from './ProjectGroup.module.css';

interface ProjectGroupProps {
  project: Project;
  sessions: Session[];
  events: Map<string, SessionEvent[]>;
}

export function ProjectGroup({ project, sessions, events }: ProjectGroupProps) {
  const [collapsed, setCollapsed] = useState(false);

  return (
    <section className={styles.group}>
      <button
        className={styles.header}
        onClick={() => setCollapsed((c) => !c)}
        aria-expanded={!collapsed}
        aria-controls={`project-sessions-${project.id}`}
      >
        <div className={styles.projectInfo}>
          <span className={styles.projectName}>{project.name}</span>
          <span className={styles.projectPath}>{project.path}</span>
        </div>
        <div className={styles.headerRight}>
          <span className={styles.count}>
            {sessions.length} session{sessions.length !== 1 ? 's' : ''}
          </span>
          <span className={styles.chevron} aria-hidden="true">
            {collapsed ? '▶' : '▼'}
          </span>
        </div>
      </button>

      {!collapsed && (
        <div
          id={`project-sessions-${project.id}`}
          className={styles.sessionList}
          role="list"
        >
          {sessions.length === 0 ? (
            <p className={styles.empty}>No sessions</p>
          ) : (
            sessions.map((session) => (
              <div key={session.id} role="listitem">
                <SessionCard
                  session={session}
                  events={events.get(session.id) ?? []}
                />
              </div>
            ))
          )}
        </div>
      )}
    </section>
  );
}
