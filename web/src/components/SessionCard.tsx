import { useState } from 'react';
import type { Session, SessionEvent } from '../types';
import { StatusBadge } from './StatusBadge';
import { AttentionIndicator } from './AttentionIndicator';
import { KillButton } from './KillButton';
import { formatDuration, uptimeSeconds } from '../utils/time';
import styles from './SessionCard.module.css';

interface SessionCardProps {
  session: Session;
  events: SessionEvent[];
}

export function SessionCard({ session, events }: SessionCardProps) {
  const [expanded, setExpanded] = useState(false);

  const shortId = session.id.slice(0, 8);
  const startedAtSecs = session.startedAt
    ? Number((session.startedAt as any).seconds ?? 0)
    : 0;
  const uptime = formatDuration(uptimeSeconds(startedAtSecs));

  return (
    <div
      className={[styles.card, expanded ? styles.expanded : ''].filter(Boolean).join(' ')}
      onClick={() => setExpanded((e) => !e)}
      role="button"
      tabIndex={0}
      aria-expanded={expanded}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') setExpanded((prev) => !prev);
      }}
    >
      <div className={styles.header}>
        <div className={styles.identity}>
          <span className={styles.sessionId} title={session.id}>
            {shortId}
          </span>
          <StatusBadge status={session.status} />
        </div>
        <div className={styles.meta}>
          <span className={styles.profile}>{session.profile}</span>
          <span className={styles.uptime}>{uptime}</span>
        </div>
      </div>

      <div className={styles.attention} onClick={(e) => e.stopPropagation()}>
        <AttentionIndicator score={session.attentionScore} />
      </div>

      <div className={styles.identity}>
        {session.tmuxWindow && (
          <span className={styles.tmuxWindow} title="tmux window">
            {session.tmuxWindow}
          </span>
        )}
        {session.tmuxSession && (
          <button
            className={styles.attachBtn}
            onClick={(e) => {
              e.stopPropagation();
              const cmd = `gru attach ${session.id.slice(0, 8)}`;
              navigator.clipboard.writeText(cmd);
            }}
            title={`Copy: gru attach ${session.id.slice(0, 8)}`}
          >
            attach
          </button>
        )}
      </div>

      {expanded && (
        <div className={styles.details} onClick={(e) => e.stopPropagation()}>
          <div className={styles.detailRow}>
            <span className={styles.detailLabel}>Full ID</span>
            <span className={styles.detailValue}>{session.id}</span>
          </div>
          <div className={styles.detailRow}>
            <span className={styles.detailLabel}>PID</span>
            <span className={styles.detailValue}>{String(session.pid)}</span>
          </div>
          <div className={styles.detailRow}>
            <span className={styles.detailLabel}>Runtime</span>
            <span className={styles.detailValue}>{session.runtime}</span>
          </div>
          {session.tmuxSession && (
            <div className={styles.detailRow}>
              <span className={styles.detailLabel}>tmux</span>
              <code className={styles.detailValue}>
                {session.tmuxSession}:{session.tmuxWindow}
              </code>
            </div>
          )}

          {events.length > 0 && (
            <div className={styles.eventTimeline}>
              <h4 className={styles.timelineTitle}>Recent Events</h4>
              <ul className={styles.eventList}>
                {events
                  .slice()
                  .reverse()
                  .map((evt) => (
                    <li key={evt.id} className={styles.eventItem}>
                      <span className={styles.eventType}>{evt.type}</span>
                      {evt.payload && (
                        <span className={styles.eventPayload}>
                          {String(evt.payload).slice(0, 120)}
                        </span>
                      )}
                    </li>
                  ))}
              </ul>
            </div>
          )}

          <div className={styles.actions}>
            <KillButton sessionId={session.id} />
          </div>
        </div>
      )}
    </div>
  );
}
