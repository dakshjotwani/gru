import { useEffect, useRef, useReducer, useCallback } from 'react';
import { gruClient } from '../client';
import type { Session, SessionEvent } from '../types';
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

function applyEvent(sessions: Map<string, Session>, event: SessionEvent): Map<string, Session> {
  const next = new Map(sessions);
  const existing = next.get(event.sessionId);
  if (!existing) return next;

  const updated: Session = {
    ...existing,
    lastEventAt: event.timestamp,
  };

  if (event.payload) {
    try {
      const raw = event.payload;
      const payloadStr = raw instanceof Uint8Array
        ? new TextDecoder().decode(raw)
        : String(raw);
      const payload = JSON.parse(payloadStr) as { status?: string };
      if (payload.status) {
        const statusMap: Record<string, SessionStatus> = {
          starting: SessionStatus.STARTING,
          running: SessionStatus.RUNNING,
          idle: SessionStatus.IDLE,
          needs_attention: SessionStatus.NEEDS_ATTENTION,
          completed: SessionStatus.COMPLETED,
          errored: SessionStatus.ERRORED,
          killed: SessionStatus.KILLED,
        };
        if (payload.status in statusMap) {
          updated.status = statusMap[payload.status];
        }
      }
    } catch {
      // Ignore unparseable payloads.
    }
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
  new Notification('Gru — Attention needed', {
    body: `Session ${session.id.slice(0, 8)} in ${projectName} needs your attention`,
    tag: `gru-attention-${session.id}`,
  });
}

export interface UseSessionStreamResult extends SessionState {
  sessionsSortedByAttention: (projectId: string) => Session[];
}

export function useSessionStream(projectId?: string): UseSessionStreamResult {
  const [state, dispatch] = useReducer(reducer, {
    sessions: new Map(),
    events: new Map(),
    connected: false,
    error: null,
  });

  const backoffRef = useRef(INITIAL_BACKOFF_MS);
  const abortRef = useRef<AbortController | null>(null);
  const prevStatusRef = useRef<Map<string, SessionStatus>>(new Map());

  const connect = useCallback(async () => {
    dispatch({ type: 'RESET' });
    abortRef.current?.abort();
    const abort = new AbortController();
    abortRef.current = abort;

    try {
      dispatch({ type: 'CONNECTED' });
      const stream = gruClient.subscribeEvents(
        { projectIds: projectId ? [projectId] : [], minAttention: 0 },
        { signal: abort.signal }
      );

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
        notifyAttention(session, session.projectId);
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
