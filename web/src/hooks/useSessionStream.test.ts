import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { act } from 'react';
import { SessionStatus } from '../types';
import type { Session, SessionEvent } from '../types';

vi.mock('../client', () => ({
  gruClient: {
    subscribeEvents: vi.fn(),
  },
}));

import { gruClient } from '../client';
import { useSessionStream } from './useSessionStream';

function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    $typeName: 'gru.v1.Session',
    id: 'session-uuid-1234',
    projectId: 'proj-1',
    runtime: 'claude-code',
    status: SessionStatus.RUNNING,
    profile: 'default',
    attentionScore: 0.5,
    startedAt: { seconds: BigInt(1000), nanos: 0 } as any,
    endedAt: undefined,
    lastEventAt: { seconds: BigInt(1001), nanos: 0 } as any,
    pid: BigInt(1234) as any,
    tmuxSession: 'gru-test',
    tmuxWindow: 'feat-dev·a1b2c3d4',
    ...overrides,
  } as Session;
}

function makeSnapshotEvent(session: Session): SessionEvent {
  return {
    id: 'evt-1',
    sessionId: session.id,
    projectId: session.projectId,
    runtime: session.runtime,
    type: 'snapshot.session',
    timestamp: session.lastEventAt,
    payload: JSON.stringify(session, (_k, v) => (typeof v === 'bigint' ? v.toString() : v)),
  } as any;
}

function makeLiveEvent(sessionId: string, type: string, payload = '{}'): SessionEvent {
  return {
    id: `evt-${Math.random()}`,
    sessionId,
    projectId: 'proj-1',
    runtime: 'claude-code',
    type,
    timestamp: { seconds: BigInt(1002), nanos: 0 } as any,
    payload,
  } as any;
}

async function* makeStream(events: SessionEvent[]): AsyncIterable<SessionEvent> {
  for (const e of events) {
    yield e;
  }
}

describe('useSessionStream', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    Object.defineProperty(globalThis, 'Notification', {
      value: { permission: 'default', requestPermission: vi.fn().mockResolvedValue('denied') },
      writable: true,
      configurable: true,
    });
    Object.defineProperty(globalThis.document, 'hasFocus', {
      value: () => true,
      writable: true,
      configurable: true,
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('processes snapshot events to populate sessions', async () => {
    const session = makeSession();
    const snapshotEvent = makeSnapshotEvent(session);

    vi.mocked(gruClient.subscribeEvents).mockReturnValue(makeStream([snapshotEvent]) as any);

    const { result } = renderHook(() => useSessionStream());

    await waitFor(() => {
      expect(result.current.sessions.size).toBe(1);
    });

    const found = result.current.sessions.get(session.id);
    expect(found).toBeDefined();
    expect(found?.id).toBe(session.id);
    expect(found?.status).toBe(SessionStatus.RUNNING);
  });

  it('appends live events to event history capped at 20', async () => {
    const session = makeSession();
    const snapshotEvent = makeSnapshotEvent(session);
    const liveEvents = Array.from({ length: 25 }, (_, i) =>
      makeLiveEvent(session.id, `event.${i}`)
    );

    vi.mocked(gruClient.subscribeEvents).mockReturnValue(
      makeStream([snapshotEvent, ...liveEvents]) as any
    );

    const { result } = renderHook(() => useSessionStream());

    await waitFor(() => {
      const evts = result.current.events.get(session.id);
      expect(evts?.length).toBe(20);
    });
  });

  it('does NOT re-derive status on live events; trusts server snapshots', async () => {
    // Rev 2 (state-pipeline): the frontend does not re-derive status
    // from event types. The session.status here stays RUNNING until a
    // fresh snapshot.session arrives carrying a new derived row.
    const session = makeSession({ status: SessionStatus.RUNNING });
    const snapshotEvent = makeSnapshotEvent(session);
    // A live event that previously would have flipped status —
    // notification.needs_attention — must NOT mutate session.status
    // anymore. The server is the single derivation point.
    const liveEvent = makeLiveEvent(session.id, 'notification.needs_attention');

    vi.mocked(gruClient.subscribeEvents).mockReturnValue(
      makeStream([snapshotEvent, liveEvent]) as any
    );

    const { result } = renderHook(() => useSessionStream());

    await waitFor(() => {
      expect(result.current.sessions.size).toBe(1);
    });

    // Status remains RUNNING because the server hasn't sent a
    // refreshed snapshot yet.
    const s = result.current.sessions.get(session.id);
    expect(s?.status).toBe(SessionStatus.RUNNING);
    // The live event is recorded in the per-session ring buffer.
    expect(result.current.events.get(session.id)?.length).toBe(1);
  });

  it('updates status when the server sends a fresh snapshot.session', async () => {
    const initial = makeSession({ status: SessionStatus.RUNNING });
    const updated = makeSession({ status: SessionStatus.NEEDS_ATTENTION });
    const initialSnap = makeSnapshotEvent(initial);
    const refreshedSnap = makeSnapshotEvent(updated);

    vi.mocked(gruClient.subscribeEvents).mockReturnValue(
      makeStream([initialSnap, refreshedSnap]) as any
    );

    const { result } = renderHook(() => useSessionStream());
    await waitFor(() => {
      const s = result.current.sessions.get(initial.id);
      expect(s?.status).toBe(SessionStatus.NEEDS_ATTENTION);
    });
  });

  it('ignores stale snapshots whose last_event_seq is older than what we have', async () => {
    // Snapshot regression guard (anti-pattern #7 / spec §3.9).
    const fresh = makeSession({
      status: SessionStatus.NEEDS_ATTENTION,
      // @ts-expect-error - lastEventSeq is added in proto for rev 2
      lastEventSeq: BigInt(10),
    });
    const stale = makeSession({
      status: SessionStatus.RUNNING,
      // @ts-expect-error - lastEventSeq is added in proto for rev 2
      lastEventSeq: BigInt(5),
    });
    const freshSnap = makeSnapshotEvent(fresh);
    const staleSnap = makeSnapshotEvent(stale);

    vi.mocked(gruClient.subscribeEvents).mockReturnValue(
      makeStream([freshSnap, staleSnap]) as any
    );
    const { result } = renderHook(() => useSessionStream());

    await waitFor(() => {
      expect(result.current.sessions.size).toBe(1);
    });
    // Final state must still reflect the FRESH snapshot — the stale
    // one with lastEventSeq=5 was dropped.
    const s = result.current.sessions.get(fresh.id);
    expect(s?.status).toBe(SessionStatus.NEEDS_ATTENTION);
  });

  it('reconnects with exponential backoff on error', async () => {
    vi.useFakeTimers();
    let callCount = 0;

    vi.mocked(gruClient.subscribeEvents).mockImplementation(() => {
      callCount++;
      // Always throw so we can observe the retry count growing.
      throw new Error('connection refused');
    });

    renderHook(() => useSessionStream());

    // Let initial effects settle (StrictMode may double-invoke effects).
    await act(async () => {
      await Promise.resolve();
    });
    const countAfterMount = callCount;
    expect(countAfterMount).toBeGreaterThanOrEqual(1);

    // Advance past the 1s backoff — expect at least one more call.
    await act(async () => {
      vi.advanceTimersByTime(1100);
      await Promise.resolve();
    });
    expect(callCount).toBeGreaterThan(countAfterMount);
  });

  it('force-reconnects on visibilitychange when disconnected, without waiting for backoff', async () => {
    // Regression test for mobile PWA resume: when iOS/Android re-foregrounds
    // the app after suspending it, we must reconnect immediately — the
    // already-scheduled backoff timer may be throttled or already-elapsed
    // while frozen.
    vi.useFakeTimers();
    let callCount = 0;

    vi.mocked(gruClient.subscribeEvents).mockImplementation(() => {
      callCount++;
      throw new Error('connection refused');
    });

    renderHook(() => useSessionStream());

    await act(async () => {
      await Promise.resolve();
    });
    const countAfterMount = callCount;
    expect(countAfterMount).toBeGreaterThanOrEqual(1);

    // Fire visibilitychange WITHOUT advancing timers. If the resume handler
    // is wired up, it cancels the pending backoff and reconnects now.
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'));
      await Promise.resolve();
    });
    expect(callCount).toBeGreaterThan(countAfterMount);
  });

  it('force-reconnects on pageshow and online events', async () => {
    vi.useFakeTimers();
    let callCount = 0;

    vi.mocked(gruClient.subscribeEvents).mockImplementation(() => {
      callCount++;
      throw new Error('connection refused');
    });

    renderHook(() => useSessionStream());

    await act(async () => {
      await Promise.resolve();
    });
    const countAfterMount = callCount;

    await act(async () => {
      window.dispatchEvent(new Event('pageshow'));
      await Promise.resolve();
    });
    const countAfterPageshow = callCount;
    expect(countAfterPageshow).toBeGreaterThan(countAfterMount);

    await act(async () => {
      window.dispatchEvent(new Event('online'));
      await Promise.resolve();
    });
    expect(callCount).toBeGreaterThan(countAfterPageshow);
  });

  it('sessionsSortedByAttention returns sessions for project sorted by score desc', async () => {
    const s1 = makeSession({ id: 'a', projectId: 'p1', attentionScore: 0.3 });
    const s2 = makeSession({ id: 'b', projectId: 'p1', attentionScore: 0.9 });
    const s3 = makeSession({ id: 'c', projectId: 'p2', attentionScore: 0.7 });

    vi.mocked(gruClient.subscribeEvents).mockReturnValue(
      makeStream([makeSnapshotEvent(s1), makeSnapshotEvent(s2), makeSnapshotEvent(s3)]) as any
    );

    const { result } = renderHook(() => useSessionStream());

    await waitFor(() => {
      expect(result.current.sessions.size).toBe(3);
    });

    const sorted = result.current.sessionsSortedByAttention('p1');
    expect(sorted.map((s) => s.id)).toEqual(['b', 'a']);
  });
});
