import { useEffect, useRef, useReducer, useCallback } from 'react';
import { gruClient } from '../client';
import type { Project, Session, SessionEvent } from '../types';
import { SessionStatus } from '../types';

export interface SessionState {
  sessions: Map<string, Session>;
  events: Map<string, SessionEvent[]>; // session_id -> last 20 events
  connected: boolean;
  error: string | null;
}

type Action =
  | { type: 'SNAPSHOT'; session: Session }
  | { type: 'EVENT'; event: SessionEvent }
  | { type: 'CONNECTED' }
  | { type: 'DISCONNECTED'; error?: string }
  | { type: 'RESET' };

// Derive session status from event type, mirroring the backend logic in
// internal/ingestion/handler.go so the frontend updates in real time without
// needing the server to echo the new status back.
function applyEvent(sessions: Map<string, Session>, event: SessionEvent): Map<string, Session> {
  const next = new Map(sessions);
  const existing = next.get(event.sessionId);
  if (!existing) return next;

  const updated: Session = {
    ...existing,
    lastEventAt: event.timestamp,
  };

  switch (event.type) {
    case 'session.start':
      updated.status = SessionStatus.RUNNING;
      break;
    case 'session.idle':
      updated.status = SessionStatus.IDLE;
      break;
    case 'session.end':
      updated.status = SessionStatus.COMPLETED;
      break;
    case 'session.crash':
      updated.status = SessionStatus.ERRORED;
      break;
    case 'session.killed':
      updated.status = SessionStatus.KILLED;
      break;
    case 'notification.needs_attention':
      updated.status = SessionStatus.NEEDS_ATTENTION;
      break;
    case 'tool.pre':
    case 'subagent.start':
      if (
        existing.status === SessionStatus.STARTING ||
        existing.status === SessionStatus.IDLE ||
        existing.status === SessionStatus.NEEDS_ATTENTION
      ) {
        updated.status = SessionStatus.RUNNING;
      }
      break;
    case 'tool.post':
    case 'tool.error':
    case 'subagent.end':
      if (existing.status === SessionStatus.STARTING) {
        updated.status = SessionStatus.RUNNING;
      }
      break;
  }

  next.set(event.sessionId, updated);
  return next;
}

function reducer(state: SessionState, action: Action): SessionState {
  switch (action.type) {
    case 'CONNECTED':
      return { ...state, connected: true, error: null };

    case 'DISCONNECTED':
      return { ...state, connected: false, error: action.error ?? null };

    case 'RESET':
      return { sessions: new Map(), events: new Map(), connected: false, error: null };

    case 'SNAPSHOT': {
      const sessions = new Map(state.sessions);
      sessions.set(action.session.id, action.session);
      return { ...state, sessions };
    }

    case 'EVENT': {
      const event = action.event;

      if (event.type === 'snapshot.session') {
        const sessions = new Map(state.sessions);
        try {
          const payloadStr = typeof event.payload === 'string'
            ? event.payload
            : new TextDecoder().decode(event.payload as Uint8Array);
          const parsed = JSON.parse(payloadStr) as Session;
          sessions.set(parsed.id, parsed);
        } catch {
          // ignore
        }
        return { ...state, sessions };
      }

      const events = new Map(state.events);
      const sessionEvents = events.get(event.sessionId) ?? [];
      const updatedEvents = [...sessionEvents, event].slice(-20);
      events.set(event.sessionId, updatedEvents);

      const sessions = applyEvent(state.sessions, event);

      return { ...state, sessions, events };
    }

    default:
      return state;
  }
}

const INITIAL_BACKOFF_MS = 1000;
const MAX_BACKOFF_MS = 30000;

function notifyAttention(session: Session, projectName: string): void {
  if (document.hasFocus()) return;
  if (Notification.permission !== 'granted') return;
  const sessionLabel = session.name || session.id.slice(0, 8);
  const body = projectName
    ? `${sessionLabel} in ${projectName} needs your attention`
    : `${sessionLabel} needs your attention`;
  new Notification('Gru — Attention needed', {
    body,
    tag: `gru-attention-${session.id}`,
  });
}

export interface UseSessionStreamResult extends SessionState {
  sessionsSortedByAttention: (projectId: string) => Session[];
}

export function useSessionStream(projectId?: string, projects?: Project[]): UseSessionStreamResult {
  const [state, dispatch] = useReducer(reducer, {
    sessions: new Map(),
    events: new Map(),
    connected: false,
    error: null,
  });

  const backoffRef = useRef(INITIAL_BACKOFF_MS);
  const abortRef = useRef<AbortController | null>(null);
  const prevStatusRef = useRef<Map<string, SessionStatus>>(new Map());
  const projectsRef = useRef<Project[]>(projects ?? []);
  projectsRef.current = projects ?? [];

  const connect = useCallback(async () => {
    abortRef.current?.abort();
    const abort = new AbortController();
    abortRef.current = abort;

    try {
      const stream = gruClient.subscribeEvents(
        { projectIds: projectId ? [projectId] : [], minAttention: 0 },
        { signal: abort.signal }
      );

      dispatch({ type: 'CONNECTED' });
      for await (const event of stream) {
        dispatch({ type: 'EVENT', event });
      }

      backoffRef.current = INITIAL_BACKOFF_MS;
    } catch (err) {
      if (abort.signal.aborted) return;
      const msg = err instanceof Error ? err.message : String(err);
      dispatch({ type: 'DISCONNECTED', error: msg });
    }

    const delay = backoffRef.current;
    backoffRef.current = Math.min(backoffRef.current * 2, MAX_BACKOFF_MS);
    setTimeout(connect, delay);
  }, [projectId]);

  useEffect(() => {
    if ('Notification' in window && Notification.permission === 'default') {
      Notification.requestPermission().catch(() => undefined);
    }
  }, []);

  useEffect(() => {
    connect();
    return () => {
      abortRef.current?.abort();
    };
  }, [connect]);

  useEffect(() => {
    for (const [id, session] of state.sessions) {
      const prev = prevStatusRef.current.get(id);
      if (
        session.status === SessionStatus.NEEDS_ATTENTION &&
        prev !== SessionStatus.NEEDS_ATTENTION
      ) {
        const project = projectsRef.current.find((p) => p.id === session.projectId);
        notifyAttention(session, project?.name ?? '');
      }
      prevStatusRef.current.set(id, session.status);
    }
  }, [state.sessions]);

  const sessionsSortedByAttention = useCallback(
    (pid: string): Session[] => {
      const result: Session[] = [];
      for (const session of state.sessions.values()) {
        if (session.projectId === pid) {
          result.push(session);
        }
      }
      return result.sort((a, b) => b.attentionScore - a.attentionScore);
    },
    [state.sessions]
  );

  return { ...state, sessionsSortedByAttention };
}
