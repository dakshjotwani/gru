import { useEffect, useMemo, useState } from 'react';
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
  onSessionSelect?: (id: string) => void;
  selectedSessionId?: string;
  /** Called whenever the visible sorted session list changes, so the parent can drive keyboard navigation. */
  onSortedSessions?: (ids: string[]) => void;
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

/** Queue ordering: the backend attention engine rolls all triage signals
 *  (paused, notification, tool_error, staleness) into a single `attention_score`.
 *  The queue mirrors that by sorting on the score itself — higher score means
 *  "the operator should look at this first." Status grouping as a primary sort
 *  key (v1) gets in the way: a stale+errored running session should rank above
 *  a newly-idle one, and score encodes that ranking natively. */
function sortSessions(sessions: Session[]): Session[] {
  return sessions.slice().sort((a, b) => {
    // Primary: attention_score desc. Proto-json omits zero-valued fields, so
    // a session whose engine score is 0 arrives as `undefined` at runtime,
    // not 0 — coerce so subtraction never yields NaN (which breaks sort).
    const as = a.attentionScore || 0;
    const bs = b.attentionScore || 0;
    if (as !== bs) return bs - as;

    // Tiebreaker within a score bucket: most recent activity first. Keeps
    // fresh sessions above stale ones when scores coincidentally match.
    const aLast = tsSeconds(a.lastEventAt);
    const bLast = tsSeconds(b.lastEventAt);
    if (aLast !== null && bLast !== null && aLast !== bLast) return bLast - aLast;
    if (aLast === null && bLast !== null) return 1;
    if (bLast === null && aLast !== null) return -1;

    // Stable final tiebreaker.
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
  });
}

export function AttentionQueue({ sessions, events, projects, connected, onSessionSelect, selectedSessionId, onSortedSessions }: AttentionQueueProps) {
  const [hideRunning, setHideRunning] = useState(false);
  const [showCompleted, setShowCompleted] = useState(false);

  const projectMap = useMemo(() => {
    const m = new Map<string, Project>();
    for (const p of projects) {
      m.set(p.id, p);
    }
    return m;
  }, [projects]);

  const { sortedSessions, runningCount, completedCount } = useMemo(() => {
    const visible: Session[] = [];
    let running = 0;
    let completed = 0;
    for (const session of sessions.values()) {
      // The Gru assistant lives outside the queue — it has its own dedicated
      // entry point ("Ask Gru") at the top of the sidebar. Treating it as a
      // minion here would confuse the queue's triage purpose.
      if (session.role === 'assistant') continue;
      if (isTerminalStatus(session.status)) {
        completed++;
        if (showCompleted) visible.push(session);
        continue;
      }
      if (session.status === SessionStatus.RUNNING) {
        running++;
        if (hideRunning) continue;
      }
      visible.push(session);
    }
    return { sortedSessions: sortSessions(visible), runningCount: running, completedCount: completed };
  }, [sessions, hideRunning, showCompleted]);

  useEffect(() => {
    onSortedSessions?.(sortedSessions.map((s) => s.id));
  }, [sortedSessions, onSortedSessions]);

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
      <div className={styles.toolbar}>
        <label className={styles.toggle}>
          <input
            type="checkbox"
            checked={showCompleted}
            onChange={() => setShowCompleted((s) => !s)}
          />
          <span className={styles.toggleLabel}>
            Show completed{completedCount > 0 ? ` (${completedCount})` : ''}
          </span>
        </label>
        <label className={styles.toggle}>
          <input
            type="checkbox"
            checked={hideRunning}
            onChange={() => setHideRunning((h) => !h)}
          />
          <span className={styles.toggleLabel}>
            Hide running{runningCount > 0 ? ` (${runningCount})` : ''}
          </span>
        </label>
      </div>
      {sortedSessions.map((session) => (
        <SessionCard
          key={session.id}
          session={session}
          events={events.get(session.id) ?? []}
          projectName={projectMap.get(session.projectId)?.name}
          onSelect={onSessionSelect}
          isSelected={selectedSessionId === session.id}
        />
      ))}
    </div>
  );
}
