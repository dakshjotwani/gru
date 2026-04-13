import { useMemo, useState } from 'react';
import type { Project, Session, SessionEvent } from '../types';
import { SessionStatus } from '../types';
import { isTerminalStatus } from '../utils/status';
import { SessionCard } from './SessionCard';
import styles from './AttentionQueue.module.css';

interface AttentionQueueProps {
  sessions: Map<string, Session>;
  events: Map<string, SessionEvent[]>;
  projects: Project[];
  connected: boolean;
}

/** Extract epoch seconds from a protobuf Timestamp or RFC3339 string, returning null if missing. */
function tsSeconds(ts: unknown): number | null {
  if (!ts) return null;
  if (typeof ts === 'object' && ts !== null && 'seconds' in ts) {
    const secs = Number((ts as { seconds: unknown }).seconds);
    if (!isNaN(secs) && secs > 0) return secs;
  }
  if (typeof ts === 'string') {
    const ms = Date.parse(ts);
    if (!isNaN(ms)) return ms / 1000;
  }
  return null;
}

const STATUS_PRIORITY: Record<number, number> = {
  [SessionStatus.NEEDS_ATTENTION]: 0,
  [SessionStatus.IDLE]: 1,
  [SessionStatus.STARTING]: 2,
  [SessionStatus.RUNNING]: 3,
};

function sortSessions(sessions: Session[]): Session[] {
  return sessions.slice().sort((a, b) => {
    // Primary: status group
    const pa = STATUS_PRIORITY[a.status] ?? 99;
    const pb = STATUS_PRIORITY[b.status] ?? 99;
    if (pa !== pb) return pa - pb;

    const aLastEvent = tsSeconds(a.lastEventAt);
    const bLastEvent = tsSeconds(b.lastEventAt);

    if (a.status === SessionStatus.NEEDS_ATTENTION) {
      // By attention_score desc, then last_event_at desc; nulls to top
      if (a.attentionScore !== b.attentionScore) return b.attentionScore - a.attentionScore;
      if (aLastEvent === null && bLastEvent === null) return 0;
      if (aLastEvent === null) return -1;
      if (bLastEvent === null) return 1;
      return bLastEvent - aLastEvent;
    }

    if (a.status === SessionStatus.RUNNING) {
      // By last_event_at desc; nulls to top
      if (aLastEvent === null && bLastEvent === null) return 0;
      if (aLastEvent === null) return -1;
      if (bLastEvent === null) return 1;
      return bLastEvent - aLastEvent;
    }

    if (a.status === SessionStatus.IDLE) {
      // By last_event_at asc (longest-idle first); nulls to top
      if (aLastEvent === null && bLastEvent === null) return 0;
      if (aLastEvent === null) return -1;
      if (bLastEvent === null) return 1;
      return aLastEvent - bLastEvent;
    }

    if (a.status === SessionStatus.STARTING) {
      // By started_at desc; nulls to top
      const aStarted = tsSeconds(a.startedAt);
      const bStarted = tsSeconds(b.startedAt);
      if (aStarted === null && bStarted === null) return 0;
      if (aStarted === null) return -1;
      if (bStarted === null) return 1;
      return bStarted - aStarted;
    }

    // Stable tiebreaker: sort by ID so equal sessions don't shuffle.
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}

export function AttentionQueue({ sessions, events, projects, connected }: AttentionQueueProps) {
  const [hideRunning, setHideRunning] = useState(false);

  const projectMap = useMemo(() => {
    const m = new Map<string, Project>();
    for (const p of projects) {
      m.set(p.id, p);
    }
    return m;
  }, [projects]);

  const { sortedSessions, runningCount } = useMemo(() => {
    const active: Session[] = [];
    let running = 0;
    for (const session of sessions.values()) {
      if (isTerminalStatus(session.status)) continue;
      if (session.status === SessionStatus.RUNNING) {
        running++;
        if (hideRunning) continue;
      }
      active.push(session);
    }
    return { sortedSessions: sortSessions(active), runningCount: running };
  }, [sessions, hideRunning]);

  if (sortedSessions.length === 0 && connected) {
    return (
      <div className={styles.empty}>
        <p className={styles.emptyText}>No active sessions. Launch an agent to get started.</p>
      </div>
    );
  }

  if (sortedSessions.length === 0 && !connected) {
    return (
      <div className={styles.empty}>
        <p className={styles.emptyText}>Connecting...</p>
      </div>
    );
  }

  return (
    <div className={styles.queue}>
      {runningCount > 0 && (
        <button
          className={styles.toggleRunning}
          onClick={() => setHideRunning((h) => !h)}
        >
          {hideRunning
            ? `Show ${runningCount} running session${runningCount !== 1 ? 's' : ''}`
            : `Hide ${runningCount} running session${runningCount !== 1 ? 's' : ''}`}
        </button>
      )}
      {sortedSessions.map((session) => (
        <SessionCard
          key={session.id}
          session={session}
          events={events.get(session.id) ?? []}
          projectName={projectMap.get(session.projectId)?.name}
        />
      ))}
    </div>
  );
}
