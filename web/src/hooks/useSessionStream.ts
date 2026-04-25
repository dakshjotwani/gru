import { useEffect, useRef, useReducer, useCallback } from 'react';
import { gruClient } from '../client';
import type { Project, Session, SessionEvent } from '../types';
import { SessionStatus } from '../types';

// State pipeline rev 2 (see
// docs/superpowers/specs/2026-04-24-state-pipeline-design.md):
//
//  - The frontend NEVER re-derives session.status from event types.
//    The server-side tailer is the single source of truth; we trust
//    `session.status` straight from snapshot/snapshot.session events.
//  - Each event carries a monotonic `seq`. We track the highest seen
//    and pass it as `since_seq` on reconnect. The server replays
//    everything that happened while we were offline.
//  - Snapshot merges are guarded by `last_event_seq` so a stale
//    snapshot can't regress local state we already have.
//  - Push notifications fire on `session.transition` events (with a
//    `to: "needs_attention"` body), not on local state diffing.

export interface SessionState {
  sessions: Map<string, Session>;
  events: Map<string, SessionEvent[]>; // session_id -> last 20 events
  connected: boolean;
  error: string | null;
  // Highest event seq the client has observed. Used as `since_seq` on
  // reconnect — the server replays anything newer.
  lastSeq: bigint;
}

type Action =
  | { type: 'EVENT'; event: SessionEvent }
  | { type: 'CONNECTED' }
  | { type: 'DISCONNECTED'; error?: string }
  | { type: 'RESET' };

function maxSeq(a: bigint, b: bigint): bigint {
  return a > b ? a : b;
}

function decodePayloadAsSession(payload: SessionEvent['payload']): Session | null {
  try {
    const payloadStr =
      typeof payload === 'string' ? payload : new TextDecoder().decode(payload as Uint8Array);
    return JSON.parse(payloadStr) as Session;
  } catch {
    return null;
  }
}

function decodeTransitionPayload(payload: SessionEvent['payload']): { from?: string; to?: string } {
  try {
    const payloadStr =
      typeof payload === 'string' ? payload : new TextDecoder().decode(payload as Uint8Array);
    return JSON.parse(payloadStr) as { from?: string; to?: string };
  } catch {
    return {};
  }
}

// Maps the state-string values the Go derivation function writes into
// session.transition payloads to the numeric SessionStatus enum the
// Session proto uses for its status field.
const transitionStatusMap: Record<string, SessionStatus> = {
  starting: SessionStatus.STARTING,
  running: SessionStatus.RUNNING,
  idle: SessionStatus.IDLE,
  needs_attention: SessionStatus.NEEDS_ATTENTION,
  completed: SessionStatus.COMPLETED,
  errored: SessionStatus.ERRORED,
  killed: SessionStatus.KILLED,
};

function reducer(state: SessionState, action: Action): SessionState {
  switch (action.type) {
    case 'CONNECTED':
      return { ...state, connected: true, error: null };

    case 'DISCONNECTED':
      return { ...state, connected: false, error: action.error ?? null };

    case 'RESET':
      return {
        sessions: new Map(),
        events: new Map(),
        connected: false,
        error: null,
        lastSeq: 0n,
      };

    case 'EVENT': {
      const event = action.event;
      const seq = BigInt(event.seq ?? 0n);

      // snapshot.session: server pushes the row at a known seq. Apply
      // only if the snapshot's last_event_seq is at least as fresh as
      // any delta we already saw for this session.
      if (event.type === 'snapshot.session') {
        const parsed = decodePayloadAsSession(event.payload);
        if (!parsed) return state;
        const sessions = new Map(state.sessions);
        const existing = sessions.get(parsed.id);
        const incomingSeq = BigInt(parsed.lastEventSeq ?? 0n);
        const existingSeq = BigInt(existing?.lastEventSeq ?? 0n);
        // Snapshot regression guard (anti-pattern #7).
        if (existing && incomingSeq < existingSeq) {
          return state; // ignore stale snapshot
        }
        sessions.set(parsed.id, parsed);
        return { ...state, sessions, lastSeq: maxSeq(state.lastSeq, seq) };
      }

      // session.deleted: drop the row.
      if (event.type === 'session.deleted') {
        const sessions = new Map(state.sessions);
        sessions.delete(event.sessionId);
        const events = new Map(state.events);
        events.delete(event.sessionId);
        return { ...state, sessions, events, lastSeq: maxSeq(state.lastSeq, seq) };
      }

      // Artifact / session-link events carry the full proto as payload —
      // re-broadcast as a window-level CustomEvent so useSessionArtifacts
      // can update local state without subscribing to the gRPC stream
      // separately. The reducer is otherwise a no-op for these.
      if (event.type === 'artifact.created') {
        if (typeof window !== 'undefined') {
          window.dispatchEvent(new CustomEvent('gru:artifact-event', { detail: { event } }));
        }
        return state;
      }
      if (event.type === 'session_link.created') {
        if (typeof window !== 'undefined') {
          window.dispatchEvent(new CustomEvent('gru:link-event', { detail: { event } }));
        }
        return state;
      }

      // session.transition: apply the server-derived status change.
      // The tailer emits this on every status flip; we trust `to` so the
      // UI never re-derives status locally. Also update lastEventSeq so
      // the snapshot regression guard rejects any stale snapshot that
      // arrives later with a lower seq.
      if (event.type === 'session.transition') {
        const body = decodeTransitionPayload(event.payload);
        const newStatus = body.to !== undefined ? transitionStatusMap[body.to] : undefined;
        const sessions = new Map(state.sessions);
        const existing = sessions.get(event.sessionId);
        if (existing && newStatus !== undefined) {
          sessions.set(event.sessionId, {
            ...existing,
            status: newStatus,
            lastEventSeq: event.seq ?? existing.lastEventSeq,
          });
        }
        const events = new Map(state.events);
        const sessionEvents = events.get(event.sessionId) ?? [];
        events.set(event.sessionId, [...sessionEvents, event].slice(-20));
        return { ...state, sessions, events, lastSeq: maxSeq(state.lastSeq, seq) };
      }

      // All other events: append to the per-session ring buffer only.
      const events = new Map(state.events);
      const sessionEvents = events.get(event.sessionId) ?? [];
      events.set(event.sessionId, [...sessionEvents, event].slice(-20));

      return { ...state, events, lastSeq: maxSeq(state.lastSeq, seq) };
    }

    default:
      return state;
  }
}

const INITIAL_BACKOFF_MS = 1000;
const MAX_BACKOFF_MS = 30000;

function notifyAttention(session: Session, projectName: string): void {
  if (document.hasFocus()) return;
  if (!('Notification' in window)) return;
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
    lastSeq: 0n,
  });

  const backoffRef = useRef(INITIAL_BACKOFF_MS);
  const abortRef = useRef<AbortController | null>(null);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isConnectedRef = useRef(false);
  const projectsRef = useRef<Project[]>(projects ?? []);
  projectsRef.current = projects ?? [];

  // Mutable ref tracking the highest seq we've shipped to the server
  // on reconnect. Lives outside React state so the reconnect callback
  // can read it without re-rendering.
  const lastSeqRef = useRef<bigint>(0n);
  // Keep lastSeqRef in sync with the reducer state.
  if (state.lastSeq !== lastSeqRef.current && state.lastSeq > lastSeqRef.current) {
    lastSeqRef.current = state.lastSeq;
  }

  const connect = useCallback(async () => {
    abortRef.current?.abort();
    const abort = new AbortController();
    abortRef.current = abort;

    if (reconnectTimerRef.current !== null) {
      clearTimeout(reconnectTimerRef.current);
      reconnectTimerRef.current = null;
    }

    try {
      const stream = gruClient.subscribeEvents(
        {
          projectIds: projectId ? [projectId] : [],
          minAttention: 0,
          sinceSeq: lastSeqRef.current,
        },
        { signal: abort.signal }
      );

      isConnectedRef.current = true;
      dispatch({ type: 'CONNECTED' });
      for await (const event of stream) {
        dispatch({ type: 'EVENT', event });
      }

      backoffRef.current = INITIAL_BACKOFF_MS;
    } catch (err) {
      if (abort.signal.aborted) return;
      const msg = err instanceof Error ? err.message : String(err);
      isConnectedRef.current = false;
      dispatch({ type: 'DISCONNECTED', error: msg });
    }

    isConnectedRef.current = false;
    const delay = backoffRef.current;
    backoffRef.current = Math.min(backoffRef.current * 2, MAX_BACKOFF_MS);
    reconnectTimerRef.current = setTimeout(() => {
      reconnectTimerRef.current = null;
      void connect();
    }, delay);
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
      if (reconnectTimerRef.current !== null) {
        clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = null;
      }
    };
  }, [connect]);

  // Resume-aware reconnect.
  useEffect(() => {
    const reconnectNow = () => {
      if (isConnectedRef.current) return;
      if (reconnectTimerRef.current !== null) {
        clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = null;
      }
      backoffRef.current = INITIAL_BACKOFF_MS;
      void connect();
    };

    const onVisibilityChange = () => {
      if (document.visibilityState === 'visible') reconnectNow();
    };
    const onPageShow = () => reconnectNow();
    const onOnline = () => reconnectNow();

    document.addEventListener('visibilitychange', onVisibilityChange);
    window.addEventListener('pageshow', onPageShow);
    window.addEventListener('online', onOnline);

    return () => {
      document.removeEventListener('visibilitychange', onVisibilityChange);
      window.removeEventListener('pageshow', onPageShow);
      window.removeEventListener('online', onOnline);
    };
  }, [connect]);

  // Tracks the highest seq for which a needs_attention notification has fired,
  // keyed by session ID. Prevents re-firing on every render when the
  // session.transition is still the last item in the ring buffer.
  const notifiedSeqRef = useRef<Map<string, bigint>>(new Map());

  // Notification trigger: fire on explicit server-emitted
  // session.transition events whose `to` is "needs_attention". The
  // server is the only thing that knows about transitions, and the
  // event includes the full {from, to, why} payload.
  useEffect(() => {
    for (const [sessionId, evts] of state.events) {
      if (evts.length === 0) continue;
      const last = evts[evts.length - 1];
      if (last.type !== 'session.transition') continue;
      const body = decodeTransitionPayload(last.payload);
      if (body.to !== 'needs_attention') continue;
      const seq = BigInt(last.seq ?? 0n);
      if (seq <= (notifiedSeqRef.current.get(sessionId) ?? 0n)) continue;
      notifiedSeqRef.current.set(sessionId, seq);
      const session = state.sessions.get(sessionId);
      if (!session) continue;
      const project = projectsRef.current.find((p) => p.id === session.projectId);
      notifyAttention(session, project?.name ?? '');
    }
  }, [state.events, state.sessions]);

  const sessionsSortedByAttention = useCallback(
    (pid: string): Session[] => {
      const result: Session[] = [];
      for (const session of state.sessions.values()) {
        if (session.projectId === pid) {
          result.push(session);
        }
      }
      return result.sort((a, b) => (b.attentionScore || 0) - (a.attentionScore || 0));
    },
    [state.sessions]
  );

  return { ...state, sessionsSortedByAttention };
}
