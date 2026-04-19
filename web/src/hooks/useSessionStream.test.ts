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

  it('updates session status from live event payload', async () => {
    const session = makeSession({ status: SessionStatus.RUNNING });
    const snapshotEvent = makeSnapshotEvent(session);
    const statusEvent = makeLiveEvent(
      session.id,
      'session.status',
      JSON.stringify({ status: 'needs_attention' })
    );

    vi.mocked(gruClient.subscribeEvents).mockReturnValue(
      makeStream([snapshotEvent, statusEvent]) as any
    );

    const { result } = renderHook(() => useSessionStream());

    await waitFor(() => {
      const s = result.current.sessions.get(session.id);
      expect(s?.status).toBe(SessionStatus.NEEDS_ATTENTION);
    });
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
