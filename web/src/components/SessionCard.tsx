import { useState, useEffect } from 'react';
import type { Session, SessionEvent } from '../types';
import { SessionStatus } from '../types';
import { gruClient } from '../client';
import { StatusBadge } from './StatusBadge';
import { KillButton } from './KillButton';
import { formatDuration, uptimeSeconds } from '../utils/time';
import { parseEventPayload } from '../utils/payload';
import styles from './SessionCard.module.css';

interface SessionCardProps {
  session: Session;
  events: SessionEvent[];
  projectName?: string;
}

function tsToEpoch(ts: unknown): number | null {
  if (!ts) return null;
  // Proto Timestamp object with seconds field (bigint or number)
  if (typeof ts === 'object' && ts !== null && 'seconds' in ts) {
    const secs = Number((ts as { seconds: unknown }).seconds);
    if (!isNaN(secs) && secs > 0) return secs;
  }
  // Protojson serializes Timestamp as an RFC3339 string
  if (typeof ts === 'string') {
    const ms = Date.parse(ts);
    if (!isNaN(ms)) return ms / 1000;
  }
  return null;
}

function getTimeInState(session: Session): string {
  const secs = tsToEpoch(session.lastEventAt);
  if (secs === null) return '';
  return formatDuration(uptimeSeconds(secs));
}

function getContextPreview(session: Session, events: SessionEvent[]): string {
  switch (session.status) {
    case SessionStatus.NEEDS_ATTENTION: {
      const evt = findLastEventOfType(events, 'notification.needs_attention');
      if (evt?.payload) {
        const parsed = parseEventPayload(evt.payload);
        if (parsed.toolName) return `Wants to use: ${parsed.toolName}`;
      }
      return 'Needs your attention';
    }
    case SessionStatus.RUNNING: {
      const evt = findLastEventOfType(events, 'tool.pre');
      if (evt?.payload) {
        const parsed = parseEventPayload(evt.payload);
        if (parsed.toolName) return `Using: ${parsed.toolName}`;
      }
      return 'Working...';
    }
    case SessionStatus.IDLE: {
      const secs = tsToEpoch(session.lastEventAt);
      if (secs !== null) {
        const mins = Math.floor(uptimeSeconds(secs) / 60);
        return `Idle for ${mins < 1 ? 'less than a minute' : `${mins} minute${mins !== 1 ? 's' : ''}`}`;
      }
      return 'Idle';
    }
    case SessionStatus.STARTING:
      return 'Starting...';
    default:
      return '';
  }
}

function findLastEventOfType(events: SessionEvent[], type: string): SessionEvent | undefined {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].type === type) return events[i];
  }
  return undefined;
}

function relativeTime(ts: unknown): string {
  const secs = tsToEpoch(ts);
  if (secs === null) return '';
  return formatDuration(uptimeSeconds(secs)) + ' ago';
}

export function SessionCard({ session, events, projectName }: SessionCardProps) {
  const [expanded, setExpanded] = useState(false);
  const [inputText, setInputText] = useState('');
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);

  // Tick every 15s to keep relative times fresh.
  const [, setTick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 15_000);
    return () => clearInterval(id);
  }, []);

  const displayName = session.name || session.id.slice(0, 8);
  const timeInState = getTimeInState(session);
  const contextPreview = getContextPreview(session, events);

  async function handleSendInput(text: string) {
    setSending(true);
    setSendError(null);
    try {
      const resp = await gruClient.sendInput({ sessionId: session.id, text });
      if (!resp.success) {
        setSendError(resp.errorMessage || 'Failed to send input');
      } else {
        setInputText('');
      }
    } catch (err) {
      setSendError(err instanceof Error ? err.message : 'Failed to send input');
    } finally {
      setSending(false);
    }
  }

  // Get context details for expanded view
  const needsAttentionContext = session.status === SessionStatus.NEEDS_ATTENTION
    ? getAttentionContext(events)
    : null;

  const idleContext = session.status === SessionStatus.IDLE
    ? getIdleContext(events)
    : null;

  const recentEvents = events.slice(-5).reverse();

  return (
    <div
      className={[
        styles.card,
        expanded ? styles.expanded : '',
        session.status === SessionStatus.NEEDS_ATTENTION ? styles.attention : '',
      ].filter(Boolean).join(' ')}
      onClick={() => setExpanded((e) => !e)}
      role="button"
      tabIndex={0}
      aria-expanded={expanded}
      onKeyDown={(e) => {
        const tag = (e.target as HTMLElement).tagName;
        if (tag === 'INPUT' || tag === 'TEXTAREA') return;
        if (e.key === 'Enter' || e.key === ' ') setExpanded((prev) => !prev);
      }}
    >
      {/* Collapsed view */}
      <div className={styles.header}>
        <div className={styles.titleRow}>
          <span className={styles.name}>{displayName}</span>
          <StatusBadge status={session.status} />
        </div>
        <div className={styles.meta}>
          {projectName && <span className={styles.project}>{projectName}</span>}
          {timeInState && <span className={styles.time}>{timeInState}</span>}
        </div>
      </div>
      <div className={styles.preview}>{contextPreview}</div>

      {/* Expanded view */}
      {expanded && (
        <div className={styles.details} onClick={(e) => e.stopPropagation()}>
          {session.description && (
            <div className={styles.section}>
              <span className={styles.label}>Description</span>
              <span className={styles.value}>{session.description}</span>
            </div>
          )}

          {session.prompt && (
            <div className={styles.section}>
              <span className={styles.label}>Prompt</span>
              <span className={styles.value}>{session.prompt}</span>
            </div>
          )}

          {needsAttentionContext && (
            <div className={styles.section}>
              <span className={styles.label}>Context</span>
              <div className={styles.contextBlock}>
                {needsAttentionContext.toolName && (
                  <div className={styles.contextLine}>Tool: {needsAttentionContext.toolName}</div>
                )}
                {needsAttentionContext.toolInput && (
                  <div className={styles.contextLine}>
                    <code className={styles.code}>{needsAttentionContext.toolInput}</code>
                  </div>
                )}
                {needsAttentionContext.message && (
                  <div className={styles.contextLine}>{needsAttentionContext.message}</div>
                )}
              </div>
            </div>
          )}

          {idleContext && (
            <div className={styles.section}>
              <span className={styles.label}>Last activity</span>
              <span className={styles.value}>{idleContext}</span>
            </div>
          )}

          {recentEvents.length > 0 && (
            <div className={styles.section}>
              <span className={styles.label}>Recent events</span>
              <ul className={styles.eventList}>
                {recentEvents.map((evt) => (
                  <li key={evt.id} className={styles.eventItem}>
                    <span className={styles.eventType}>{evt.type}</span>
                    <span className={styles.eventTime}>{relativeTime(evt.timestamp)}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {/* Actions */}
          <div className={styles.actions}>
            {session.status === SessionStatus.NEEDS_ATTENTION && (
              <>
                <button
                  className={styles.approveBtn}
                  onClick={() => handleSendInput('y')}
                  disabled={sending}
                >
                  Approve
                </button>
                <button
                  className={styles.denyBtn}
                  onClick={() => handleSendInput('n')}
                  disabled={sending}
                >
                  Deny
                </button>
              </>
            )}

            {(session.status === SessionStatus.NEEDS_ATTENTION ||
              session.status === SessionStatus.IDLE) && (
              <div className={styles.inputRow}>
                <input
                  className={styles.textInput}
                  type="text"
                  placeholder={session.status === SessionStatus.IDLE ? 'Send prompt...' : 'Custom response...'}
                  value={inputText}
                  onChange={(e) => setInputText(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && inputText.trim()) {
                      e.stopPropagation();
                      handleSendInput(inputText.trim());
                    }
                  }}
                  onClick={(e) => e.stopPropagation()}
                  disabled={sending}
                />
                <button
                  className={styles.sendBtn}
                  onClick={() => {
                    if (inputText.trim()) handleSendInput(inputText.trim());
                  }}
                  disabled={sending || !inputText.trim()}
                >
                  Send
                </button>
              </div>
            )}

            {session.tmuxSession && (
              <button
                className={styles.attachBtn}
                onClick={() => {
                  const cmd = `tmux attach -t ${session.tmuxSession}`;
                  navigator.clipboard.writeText(cmd);
                }}
                title={`Copy: tmux attach -t ${session.tmuxSession}`}
              >
                Attach
              </button>
            )}

            <KillButton sessionId={session.id} />

            {sendError && <span className={styles.error}>{sendError}</span>}
          </div>
        </div>
      )}
    </div>
  );
}

function getAttentionContext(events: SessionEvent[]): { toolName?: string; toolInput?: string; message?: string } | null {
  const evt = findLastEventOfType(events, 'notification.needs_attention');
  if (!evt?.payload) return null;
  const parsed = parseEventPayload(evt.payload);
  if (!parsed.toolName && !parsed.message) return null;
  return parsed;
}

function getIdleContext(events: SessionEvent[]): string | null {
  // Look for the last event that has a message
  for (let i = events.length - 1; i >= 0; i--) {
    const evt = events[i];
    if (evt.payload) {
      const parsed = parseEventPayload(evt.payload);
      if (parsed.message) return parsed.message;
    }
  }
  return null;
}
