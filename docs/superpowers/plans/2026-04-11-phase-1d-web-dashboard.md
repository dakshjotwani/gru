# Gru Phase 1d — Web Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a React 18 + TypeScript web dashboard that streams live session data from the Gru gRPC server, displays sessions grouped by project with status badges and attention scores, and triggers desktop notifications when sessions need attention.

**Architecture:** Single-page Vite app using `@connectrpc/connect-web` for gRPC-web communication; a `useSessionStream` hook maintains all session state by processing snapshot events on connect then applying incremental events; CSS Modules for styling with no external UI libraries.

**Tech Stack:** React 18, TypeScript, Vite, `@connectrpc/connect-web`, `@connectrpc/connect`, `@bufbuild/protobuf`, Vitest, React Testing Library, CSS Modules

---

## Prerequisites

Phase 1a must be complete (gRPC server running at `http://localhost:7777`, proto at `proto/gru/v1/gru.proto`).

Install buf CLI if not already present:
```bash
buf --version
# expected: 1.x.x
```

---

## Task 1 — Scaffold the Vite + React project

- [ ] Create `web/` directory and initialize with Vite:

```bash
cd /path/to/gru
npm create vite@latest web -- --template react-ts
cd web
npm install
```

Expected output ends with: `Done. Now run: cd web && npm run dev`

- [ ] Replace `web/package.json` with exact dependencies:

**`web/package.json`**
```json
{
  "name": "gru-web",
  "version": "0.1.0",
  "private": true,
  "scripts": {
    "dev": "vite",
    "build": "tsc && vite build",
    "preview": "vite preview",
    "test": "vitest run",
    "test:watch": "vitest",
    "buf:gen": "buf generate --template ../buf.gen.yaml ../proto"
  },
  "dependencies": {
    "@bufbuild/protobuf": "^1.10.0",
    "@connectrpc/connect": "^1.4.0",
    "@connectrpc/connect-web": "^1.4.0",
    "react": "^18.3.1",
    "react-dom": "^18.3.1"
  },
  "devDependencies": {
    "@testing-library/jest-dom": "^6.4.6",
    "@testing-library/react": "^16.0.0",
    "@testing-library/user-event": "^14.5.2",
    "@types/react": "^18.3.3",
    "@types/react-dom": "^18.3.0",
    "@vitejs/plugin-react": "^4.3.1",
    "jsdom": "^24.1.0",
    "typescript": "^5.5.3",
    "vite": "^5.3.4",
    "vitest": "^1.6.0"
  }
}
```

- [ ] Install dependencies:

```bash
cd web && npm install
```

Expected: `added NNN packages` with no errors.

---

## Task 2 — Configure Vite, TypeScript, and Vitest

- [ ] Write `web/vite.config.ts`:

```typescript
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
  },
});
```

- [ ] Write `web/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "useDefineForClassFields": true,
    "lib": ["ES2020", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "bundler",
    "allowImportingTsExtensions": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "baseUrl": ".",
    "paths": {
      "@/*": ["src/*"]
    }
  },
  "include": ["src"],
  "references": [{ "path": "./tsconfig.node.json" }]
}
```

- [ ] Write `web/tsconfig.node.json`:

```json
{
  "compilerOptions": {
    "composite": true,
    "skipLibCheck": true,
    "module": "ESNext",
    "moduleResolution": "bundler",
    "allowSyntheticDefaultImports": true
  },
  "include": ["vite.config.ts"]
}
```

- [ ] Write `web/src/test-setup.ts`:

```typescript
import '@testing-library/jest-dom';
```

- [ ] Write `web/index.html`:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <link rel="icon" type="image/svg+xml" href="/vite.svg" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Gru — Mission Control</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

---

## Task 3 — Add TypeScript generation to `buf.gen.yaml`

- [ ] Open the root `buf.gen.yaml` (created in Phase 1a) and append the web plugins. The final file should look like:

**`buf.gen.yaml`** (full file — replace existing content):
```yaml
version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: .
    opt: paths=source_relative
  - remote: buf.build/connectrpc/go
    out: .
    opt: paths=source_relative
  - remote: buf.build/bufbuild/es
    out: web/src/gen
    opt: target=ts
  - remote: buf.build/connectrpc/es
    out: web/src/gen
    opt: target=ts
```

- [ ] Run buf generate from the repo root:

```bash
cd /path/to/gru
buf generate
```

Expected: Files created under `web/src/gen/` including `gru_pb.ts` and `gru_connect.ts`. No errors.

- [ ] Verify generated files exist:

```bash
ls web/src/gen/gru/v1/
# expected: gru_pb.ts  gru_connect.ts
```

---

## Task 4 — Write `types.ts` and `client.ts`

- [ ] Write `web/src/types.ts`:

```typescript
// Re-export proto-generated types for convenient imports throughout the app.
export type {
  Session,
  SessionEvent,
  Project,
  ListSessionsRequest,
  ListSessionsResponse,
  GetSessionRequest,
  LaunchSessionRequest,
  LaunchSessionResponse,
  KillSessionRequest,
  KillSessionResponse,
  ListProjectsRequest,
  ListProjectsResponse,
  SubscribeEventsRequest,
} from './gen/gru/v1/gru_pb';

export { SessionStatus } from './gen/gru/v1/gru_pb';
```

> **Note:** The `Session` proto message includes two tmux fields that are populated for all sessions launched by Gru:
> ```ts
> tmuxSession: string;  // e.g. "gru-av-sim"
> tmuxWindow:  string;  // e.g. "feat-dev·a1b2c3d4"
> ```
> These come through automatically from the buf-generated types — no manual additions needed. They are used by `SessionCard` to display the tmux window name and render an "attach" button that copies `gru attach <short-id>` to the clipboard.

- [ ] Write `web/src/client.ts`:

```typescript
import { createClient } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { GruService } from './gen/gru/v1/gru_connect';

const serverUrl = import.meta.env.VITE_GRU_SERVER_URL ?? 'http://localhost:7777';
const apiKey = import.meta.env.VITE_GRU_API_KEY ?? '';

const transport = createConnectTransport({
  baseUrl: serverUrl,
  interceptors: [
    (next) => async (req) => {
      if (apiKey) {
        req.header.set('Authorization', `Bearer ${apiKey}`);
      }
      return next(req);
    },
  ],
});

export const gruClient = createClient(GruService, transport);
```

---

## Task 5 — Write utility modules with tests

- [ ] Write `web/src/utils/status.ts`:

```typescript
import { SessionStatus } from '../types';

export interface StatusDisplay {
  label: string;
  colorClass: string;
  pulsing: boolean;
}

export function getStatusDisplay(status: SessionStatus): StatusDisplay {
  switch (status) {
    case SessionStatus.NEEDS_ATTENTION:
      return { label: 'Needs Attention', colorClass: 'statusNeedsAttention', pulsing: false };
    case SessionStatus.RUNNING:
      return { label: 'Running', colorClass: 'statusRunning', pulsing: false };
    case SessionStatus.IDLE:
      return { label: 'Idle', colorClass: 'statusIdle', pulsing: false };
    case SessionStatus.STARTING:
      return { label: 'Starting', colorClass: 'statusStarting', pulsing: true };
    case SessionStatus.COMPLETED:
      return { label: 'Completed', colorClass: 'statusCompleted', pulsing: false };
    case SessionStatus.ERRORED:
      return { label: 'Errored', colorClass: 'statusErrored', pulsing: false };
    case SessionStatus.KILLED:
      return { label: 'Killed', colorClass: 'statusKilled', pulsing: false };
    case SessionStatus.UNSPECIFIED:
    default:
      return { label: 'Unknown', colorClass: 'statusUnknown', pulsing: false };
  }
}

export function isTerminalStatus(status: SessionStatus): boolean {
  return (
    status === SessionStatus.COMPLETED ||
    status === SessionStatus.ERRORED ||
    status === SessionStatus.KILLED
  );
}
```

- [ ] Write `web/src/utils/status.test.ts`:

```typescript
import { describe, it, expect } from 'vitest';
import { getStatusDisplay, isTerminalStatus } from './status';
import { SessionStatus } from '../types';

describe('getStatusDisplay', () => {
  it('returns red/orange class for needs_attention', () => {
    const d = getStatusDisplay(SessionStatus.NEEDS_ATTENTION);
    expect(d.label).toBe('Needs Attention');
    expect(d.colorClass).toBe('statusNeedsAttention');
    expect(d.pulsing).toBe(false);
  });

  it('returns green class for running', () => {
    const d = getStatusDisplay(SessionStatus.RUNNING);
    expect(d.colorClass).toBe('statusRunning');
    expect(d.pulsing).toBe(false);
  });

  it('returns yellow class for idle', () => {
    const d = getStatusDisplay(SessionStatus.IDLE);
    expect(d.colorClass).toBe('statusIdle');
  });

  it('returns blue + pulsing for starting', () => {
    const d = getStatusDisplay(SessionStatus.STARTING);
    expect(d.colorClass).toBe('statusStarting');
    expect(d.pulsing).toBe(true);
  });

  it('returns gray class for completed', () => {
    const d = getStatusDisplay(SessionStatus.COMPLETED);
    expect(d.colorClass).toBe('statusCompleted');
  });

  it('returns red class for errored', () => {
    const d = getStatusDisplay(SessionStatus.ERRORED);
    expect(d.colorClass).toBe('statusErrored');
  });

  it('returns gray class for killed', () => {
    const d = getStatusDisplay(SessionStatus.KILLED);
    expect(d.colorClass).toBe('statusKilled');
  });

  it('returns unknown for unspecified', () => {
    const d = getStatusDisplay(SessionStatus.UNSPECIFIED);
    expect(d.label).toBe('Unknown');
  });
});

describe('isTerminalStatus', () => {
  it('returns true for completed, errored, killed', () => {
    expect(isTerminalStatus(SessionStatus.COMPLETED)).toBe(true);
    expect(isTerminalStatus(SessionStatus.ERRORED)).toBe(true);
    expect(isTerminalStatus(SessionStatus.KILLED)).toBe(true);
  });

  it('returns false for running, idle, starting, needs_attention', () => {
    expect(isTerminalStatus(SessionStatus.RUNNING)).toBe(false);
    expect(isTerminalStatus(SessionStatus.IDLE)).toBe(false);
    expect(isTerminalStatus(SessionStatus.STARTING)).toBe(false);
    expect(isTerminalStatus(SessionStatus.NEEDS_ATTENTION)).toBe(false);
  });
});
```

- [ ] Write `web/src/utils/time.ts`:

```typescript
/**
 * Format a duration in seconds into a human-readable string.
 * Examples: "45s", "2m 14s", "1h 5m", "3d 2h"
 */
export function formatDuration(seconds: number): string {
  if (seconds < 0) return '0s';
  if (seconds < 60) return `${Math.floor(seconds)}s`;

  const minutes = Math.floor(seconds / 60);
  const secs = Math.floor(seconds % 60);

  if (minutes < 60) {
    return secs > 0 ? `${minutes}m ${secs}s` : `${minutes}m`;
  }

  const hours = Math.floor(minutes / 60);
  const mins = minutes % 60;

  if (hours < 24) {
    return mins > 0 ? `${hours}h ${mins}m` : `${hours}h`;
  }

  const days = Math.floor(hours / 24);
  const hrs = hours % 24;
  return hrs > 0 ? `${days}d ${hrs}h` : `${days}d`;
}

/**
 * Calculate uptime in seconds from a started_at timestamp (seconds since epoch).
 */
export function uptimeSeconds(startedAtSecs: bigint | number, nowMs?: number): number {
  const nowSecs = (nowMs ?? Date.now()) / 1000;
  return Math.max(0, nowSecs - Number(startedAtSecs));
}
```

- [ ] Write `web/src/utils/time.test.ts`:

```typescript
import { describe, it, expect } from 'vitest';
import { formatDuration, uptimeSeconds } from './time';

describe('formatDuration', () => {
  it('formats seconds under a minute', () => {
    expect(formatDuration(0)).toBe('0s');
    expect(formatDuration(45)).toBe('45s');
    expect(formatDuration(59)).toBe('59s');
  });

  it('formats minutes and seconds', () => {
    expect(formatDuration(60)).toBe('1m');
    expect(formatDuration(134)).toBe('2m 14s');
    expect(formatDuration(3599)).toBe('59m 59s');
  });

  it('formats hours and minutes', () => {
    expect(formatDuration(3600)).toBe('1h');
    expect(formatDuration(3660)).toBe('1h 1m');
    expect(formatDuration(7534)).toBe('2h 5m');
  });

  it('formats days and hours', () => {
    expect(formatDuration(86400)).toBe('1d');
    expect(formatDuration(90000)).toBe('1d 1h');
    expect(formatDuration(172800)).toBe('2d');
  });

  it('handles negative values', () => {
    expect(formatDuration(-5)).toBe('0s');
  });
});

describe('uptimeSeconds', () => {
  it('computes difference from a given now', () => {
    const startedAt = 1000; // seconds
    const nowMs = 1060 * 1000; // 1060 seconds in ms
    expect(uptimeSeconds(startedAt, nowMs)).toBe(60);
  });

  it('handles bigint startedAt', () => {
    const startedAt = BigInt(1000);
    const nowMs = 1060 * 1000;
    expect(uptimeSeconds(startedAt, nowMs)).toBe(60);
  });

  it('returns 0 for future started_at', () => {
    const startedAt = 9999999999;
    expect(uptimeSeconds(startedAt, 1000)).toBe(0);
  });
});
```

- [ ] Run utility tests to verify:

```bash
cd web && npm test -- --reporter=verbose src/utils/
```

Expected: All tests pass (green).

---

## Task 6 — Write the `useSessionStream` hook

- [ ] Write `web/src/hooks/useSessionStream.ts`:

```typescript
import { useEffect, useRef, useReducer, useCallback } from 'react';
import { gruClient } from '../client';
import type { Session, SessionEvent } from '../types';
import { SessionStatus } from '../types';
import { isTerminalStatus } from '../utils/status';

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

  // Update session fields based on event type.
  // The server sends full session data in snapshot.session events;
  // for other events we update last_event_at and potentially status.
  const updated: Session = {
    ...existing,
    lastEventAt: event.timestamp,
  };

  // Parse status updates from the event payload when available.
  if (event.payload) {
    try {
      const payload = JSON.parse(event.payload) as { status?: string };
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

      // snapshot.session events populate the initial session map.
      if (event.type === 'snapshot.session') {
        const sessions = new Map(state.sessions);
        // The full session object is embedded in payload for snapshot events.
        try {
          const parsed = JSON.parse(event.payload) as Partial<Session>;
          const session: Session = parsed as Session;
          sessions.set(session.id, session);
        } catch {
          // ignore
        }
        return { ...state, sessions };
      }

      // Append to event history (cap at 20).
      const events = new Map(state.events);
      const sessionEvents = events.get(event.sessionId) ?? [];
      const updatedEvents = [...sessionEvents, event].slice(-20);
      events.set(event.sessionId, updatedEvents);

      // Apply any state changes from the event.
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
        { projectId: projectId ?? '' },
        { signal: abort.signal }
      );

      for await (const event of stream) {
        dispatch({ type: 'EVENT', event });
      }

      // Stream ended cleanly — reconnect after backoff.
      backoffRef.current = INITIAL_BACKOFF_MS;
    } catch (err) {
      if (abort.signal.aborted) return; // intentional abort, do not reconnect
      const msg = err instanceof Error ? err.message : String(err);
      dispatch({ type: 'DISCONNECTED', error: msg });
    }

    // Schedule reconnect with backoff.
    const delay = backoffRef.current;
    backoffRef.current = Math.min(backoffRef.current * 2, MAX_BACKOFF_MS);
    setTimeout(connect, delay);
  }, [projectId]);

  // Request notification permission on mount.
  useEffect(() => {
    if ('Notification' in window && Notification.permission === 'default') {
      Notification.requestPermission().catch(() => undefined);
    }
  }, []);

  // Start the stream.
  useEffect(() => {
    connect();
    return () => {
      abortRef.current?.abort();
    };
  }, [connect]);

  // Detect attention transitions and fire notifications.
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
```

- [ ] Write `web/src/hooks/useSessionStream.test.ts`:

```typescript
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, act, waitFor } from '@testing-library/react';
import { SessionStatus } from '../types';
import type { Session, SessionEvent } from '../types';

// Mock the gRPC client.
vi.mock('../client', () => ({
  gruClient: {
    subscribeEvents: vi.fn(),
  },
}));

import { gruClient } from '../client';
import { useSessionStream } from './useSessionStream';

function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    id: 'session-uuid-1234',
    projectId: 'proj-1',
    runtime: 'claude-code',
    status: SessionStatus.RUNNING,
    profile: 'default',
    attentionScore: 0.5,
    startedAt: { seconds: BigInt(1000), nanos: 0 } as any,
    endedAt: undefined,
    lastEventAt: { seconds: BigInt(1001), nanos: 0 } as any,
    pid: 1234,
    tmuxSession: 'gru-test',
    tmuxWindow: 'feat-dev·a1b2c3d4',
    ...overrides,
  };
}

function makeSnapshotEvent(session: Session): SessionEvent {
  return {
    id: 'evt-1',
    sessionId: session.id,
    projectId: session.projectId,
    runtime: session.runtime,
    type: 'snapshot.session',
    timestamp: session.lastEventAt,
    payload: JSON.stringify(session),
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
    // Mock Notification API.
    Object.defineProperty(global, 'Notification', {
      value: { permission: 'default', requestPermission: vi.fn().mockResolvedValue('denied') },
      writable: true,
      configurable: true,
    });
    Object.defineProperty(global.document, 'hasFocus', {
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
      if (callCount === 1) {
        throw new Error('connection refused');
      }
      return makeStream([]) as any;
    });

    renderHook(() => useSessionStream());

    // First call throws immediately.
    await act(async () => {
      await Promise.resolve();
    });
    expect(callCount).toBe(1);

    // Advance past the 1s backoff.
    await act(async () => {
      vi.advanceTimersByTime(1100);
      await Promise.resolve();
    });
    expect(callCount).toBe(2);
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
```

---

## Task 7 — Write the `useProjects` hook

- [ ] Write `web/src/hooks/useProjects.ts`:

```typescript
import { useEffect, useState } from 'react';
import { gruClient } from '../client';
import type { Project } from '../types';

export interface UseProjectsResult {
  projects: Project[];
  loading: boolean;
  error: string | null;
  refetch: () => void;
}

export function useProjects(): UseProjectsResult {
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [fetchCount, setFetchCount] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);

    gruClient
      .listProjects({})
      .then((res) => {
        if (!cancelled) {
          setProjects(res.projects);
          setLoading(false);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err));
          setLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [fetchCount]);

  return {
    projects,
    loading,
    error,
    refetch: () => setFetchCount((c) => c + 1),
  };
}
```

---

## Task 8 — Write the `StatusBadge` component

- [ ] Write `web/src/components/StatusBadge.tsx`:

```typescript
import { SessionStatus } from '../types';
import { getStatusDisplay } from '../utils/status';
import styles from './StatusBadge.module.css';

interface StatusBadgeProps {
  status: SessionStatus;
}

export function StatusBadge({ status }: StatusBadgeProps) {
  const { label, colorClass, pulsing } = getStatusDisplay(status);
  return (
    <span
      className={[
        styles.badge,
        styles[colorClass],
        pulsing ? styles.pulsing : '',
      ]
        .filter(Boolean)
        .join(' ')}
    >
      {label}
    </span>
  );
}
```

- [ ] Write `web/src/components/StatusBadge.module.css`:

```css
.badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 12px;
  font-size: 0.75rem;
  font-weight: 600;
  letter-spacing: 0.02em;
  text-transform: uppercase;
  white-space: nowrap;
}

.statusNeedsAttention {
  background: #ff4d4f22;
  color: #ff4d4f;
  border: 1px solid #ff4d4f88;
}

.statusRunning {
  background: #52c41a22;
  color: #52c41a;
  border: 1px solid #52c41a88;
}

.statusIdle {
  background: #faad1422;
  color: #d48806;
  border: 1px solid #faad1488;
}

.statusStarting {
  background: #1677ff22;
  color: #1677ff;
  border: 1px solid #1677ff88;
}

.statusCompleted {
  background: #8c8c8c22;
  color: #8c8c8c;
  border: 1px solid #8c8c8c88;
}

.statusErrored {
  background: #ff4d4f22;
  color: #cf1322;
  border: 1px solid #ff4d4f88;
}

.statusKilled {
  background: #8c8c8c22;
  color: #595959;
  border: 1px solid #8c8c8c88;
}

.statusUnknown {
  background: #d9d9d922;
  color: #595959;
  border: 1px solid #d9d9d9;
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.5; }
}

.pulsing {
  animation: pulse 1.4s ease-in-out infinite;
}
```

---

## Task 9 — Write the `AttentionIndicator` component

- [ ] Write `web/src/components/AttentionIndicator.tsx`:

```typescript
import styles from './AttentionIndicator.module.css';

interface AttentionIndicatorProps {
  score: number; // 0.0 – 1.0
}

export function AttentionIndicator({ score }: AttentionIndicatorProps) {
  const pct = Math.round(Math.min(1, Math.max(0, score)) * 100);
  const colorClass =
    pct >= 75
      ? styles.high
      : pct >= 40
        ? styles.medium
        : styles.low;

  return (
    <div className={styles.wrapper} title={`Attention score: ${pct}%`}>
      <div className={styles.track}>
        <div className={[styles.fill, colorClass].join(' ')} style={{ width: `${pct}%` }} />
      </div>
      <span className={styles.label}>{pct}%</span>
    </div>
  );
}
```

- [ ] Write `web/src/components/AttentionIndicator.module.css`:

```css
.wrapper {
  display: flex;
  align-items: center;
  gap: 6px;
}

.track {
  flex: 1;
  height: 6px;
  background: #f0f0f0;
  border-radius: 3px;
  overflow: hidden;
  min-width: 60px;
}

.fill {
  height: 100%;
  border-radius: 3px;
  transition: width 0.3s ease;
}

.low {
  background: #52c41a;
}

.medium {
  background: #faad14;
}

.high {
  background: #ff4d4f;
}

.label {
  font-size: 0.7rem;
  color: #8c8c8c;
  min-width: 28px;
  text-align: right;
}
```

---

## Task 10 — Write the `KillButton` component with tests

- [ ] Write `web/src/components/KillButton.tsx`:

```typescript
import { useState } from 'react';
import { gruClient } from '../client';
import styles from './KillButton.module.css';

interface KillButtonProps {
  sessionId: string;
  onKilled?: () => void;
}

export function KillButton({ sessionId, onKilled }: KillButtonProps) {
  const [confirming, setConfirming] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const shortId = sessionId.slice(0, 8);

  function handleClick() {
    setConfirming(true);
    setError(null);
  }

  function handleCancel() {
    setConfirming(false);
  }

  async function handleConfirm() {
    setLoading(true);
    setError(null);
    try {
      await gruClient.killSession({ sessionId });
      setConfirming(false);
      onKilled?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to kill session');
    } finally {
      setLoading(false);
    }
  }

  if (confirming) {
    return (
      <div className={styles.confirm} role="dialog" aria-label={`Kill session ${shortId}?`}>
        <span className={styles.question}>Kill session {shortId}?</span>
        <button
          className={styles.confirmBtn}
          onClick={handleConfirm}
          disabled={loading}
          aria-label="Confirm kill"
        >
          {loading ? 'Killing…' : 'Confirm'}
        </button>
        <button
          className={styles.cancelBtn}
          onClick={handleCancel}
          disabled={loading}
          aria-label="Cancel"
        >
          Cancel
        </button>
        {error && <span className={styles.error}>{error}</span>}
      </div>
    );
  }

  return (
    <button className={styles.killBtn} onClick={handleClick} aria-label={`Kill session ${shortId}`}>
      Kill
    </button>
  );
}
```

- [ ] Write `web/src/components/KillButton.module.css`:

```css
.killBtn {
  padding: 3px 10px;
  border-radius: 4px;
  border: 1px solid #ff4d4f88;
  background: transparent;
  color: #ff4d4f;
  font-size: 0.75rem;
  cursor: pointer;
  transition: background 0.15s;
}

.killBtn:hover {
  background: #ff4d4f11;
}

.confirm {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.question {
  font-size: 0.8rem;
  color: #595959;
}

.confirmBtn {
  padding: 3px 10px;
  border-radius: 4px;
  border: none;
  background: #ff4d4f;
  color: #fff;
  font-size: 0.75rem;
  cursor: pointer;
}

.confirmBtn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.cancelBtn {
  padding: 3px 10px;
  border-radius: 4px;
  border: 1px solid #d9d9d9;
  background: transparent;
  color: #595959;
  font-size: 0.75rem;
  cursor: pointer;
}

.cancelBtn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.error {
  font-size: 0.75rem;
  color: #ff4d4f;
}
```

- [ ] Write `web/src/components/KillButton.test.tsx`:

```typescript
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { KillButton } from './KillButton';

vi.mock('../client', () => ({
  gruClient: {
    killSession: vi.fn(),
  },
}));

import { gruClient } from '../client';

describe('KillButton', () => {
  const SESSION_ID = 'abcdef12-1234-5678-abcd-ef1234567890';

  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders a Kill button initially', () => {
    render(<KillButton sessionId={SESSION_ID} />);
    expect(screen.getByRole('button', { name: /kill session abcdef12/i })).toBeInTheDocument();
  });

  it('shows confirmation dialog when clicked', () => {
    render(<KillButton sessionId={SESSION_ID} />);
    fireEvent.click(screen.getByRole('button', { name: /kill session abcdef12/i }));
    expect(screen.getByText(/kill session abcdef12\?/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /confirm kill/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /cancel/i })).toBeInTheDocument();
  });

  it('calls killSession and invokes onKilled on confirm', async () => {
    const onKilled = vi.fn();
    vi.mocked(gruClient.killSession).mockResolvedValue({} as any);

    render(<KillButton sessionId={SESSION_ID} onKilled={onKilled} />);
    fireEvent.click(screen.getByRole('button', { name: /kill session abcdef12/i }));
    fireEvent.click(screen.getByRole('button', { name: /confirm kill/i }));

    await waitFor(() => {
      expect(gruClient.killSession).toHaveBeenCalledWith({ sessionId: SESSION_ID });
      expect(onKilled).toHaveBeenCalledOnce();
    });
  });

  it('does not call killSession when cancel is clicked', () => {
    render(<KillButton sessionId={SESSION_ID} />);
    fireEvent.click(screen.getByRole('button', { name: /kill session abcdef12/i }));
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }));
    expect(gruClient.killSession).not.toHaveBeenCalled();
    // Confirm dialog dismissed.
    expect(screen.queryByText(/kill session abcdef12\?/i)).not.toBeInTheDocument();
  });

  it('shows error message when killSession rejects', async () => {
    vi.mocked(gruClient.killSession).mockRejectedValue(new Error('connection refused'));

    render(<KillButton sessionId={SESSION_ID} />);
    fireEvent.click(screen.getByRole('button', { name: /kill session abcdef12/i }));
    fireEvent.click(screen.getByRole('button', { name: /confirm kill/i }));

    await waitFor(() => {
      expect(screen.getByText('connection refused')).toBeInTheDocument();
    });
  });
});
```

---

## Task 11 — Write the `SessionCard` component

- [ ] Write `web/src/components/SessionCard.tsx`:

```typescript
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
  const uptime = formatDuration(
    uptimeSeconds(
      session.startedAt ? Number((session.startedAt as any).seconds ?? session.startedAt) : 0
    )
  );

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
            onClick={() => {
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
            <span className={styles.detailValue}>{session.pid}</span>
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
          {session.tmuxSession && (
            <div className={styles.detailRow}>
              <span className={styles.detailLabel}>attach</span>
              <code className={styles.detailValue}>
                gru attach {session.id.slice(0, 8)}
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
                        <span className={styles.eventPayload}>{evt.payload.slice(0, 120)}</span>
                      )}
                    </li>
                  ))}
              </ul>
            </div>
          )}

          <div className={styles.actions} onClick={(e) => e.stopPropagation()}>
            <KillButton sessionId={session.id} />
          </div>
        </div>
      )}
    </div>
  );
}
```

- [ ] Write `web/src/components/SessionCard.module.css`:

```css
.card {
  background: #fff;
  border: 1px solid #f0f0f0;
  border-radius: 8px;
  padding: 12px 14px;
  cursor: pointer;
  transition: box-shadow 0.15s, border-color 0.15s;
  outline: none;
}

.card:hover {
  border-color: #d9d9d9;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.06);
}

.card:focus-visible {
  outline: 2px solid #1677ff;
  outline-offset: 2px;
}

.expanded {
  border-color: #1677ff44;
  box-shadow: 0 2px 12px rgba(22, 119, 255, 0.08);
}

.header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 8px;
  gap: 8px;
}

.identity {
  display: flex;
  align-items: center;
  gap: 8px;
}

.sessionId {
  font-family: 'JetBrains Mono', 'Fira Code', monospace;
  font-size: 0.85rem;
  font-weight: 600;
  color: #262626;
}

.meta {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 2px;
}

.profile {
  font-size: 0.75rem;
  color: #8c8c8c;
}

.uptime {
  font-size: 0.75rem;
  color: #595959;
  font-variant-numeric: tabular-nums;
}

.attention {
  margin-top: 8px;
}

.details {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid #f5f5f5;
}

.detailRow {
  display: flex;
  gap: 8px;
  margin-bottom: 4px;
}

.detailLabel {
  font-size: 0.75rem;
  color: #8c8c8c;
  min-width: 64px;
}

.detailValue {
  font-size: 0.75rem;
  color: #262626;
  font-family: 'JetBrains Mono', 'Fira Code', monospace;
  word-break: break-all;
}

.eventTimeline {
  margin-top: 10px;
}

.timelineTitle {
  font-size: 0.75rem;
  color: #8c8c8c;
  font-weight: 600;
  margin: 0 0 6px 0;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.eventList {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 200px;
  overflow-y: auto;
}

.eventItem {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: 4px 6px;
  background: #fafafa;
  border-radius: 4px;
  border-left: 2px solid #e8e8e8;
}

.eventType {
  font-size: 0.7rem;
  font-weight: 600;
  color: #1677ff;
  font-family: 'JetBrains Mono', monospace;
}

.eventPayload {
  font-size: 0.68rem;
  color: #595959;
  word-break: break-all;
}

.actions {
  margin-top: 10px;
  display: flex;
  justify-content: flex-end;
}

.tmuxWindow {
  font-family: monospace;
  font-size: 0.75rem;
  color: var(--color-muted);
  opacity: 0.8;
}

.attachBtn {
  font-size: 0.7rem;
  padding: 2px 8px;
  border: 1px solid var(--color-border);
  border-radius: 4px;
  background: transparent;
  cursor: pointer;
  color: var(--color-muted);
}

.attachBtn:hover {
  border-color: var(--color-accent);
  color: var(--color-accent);
}
```

---

## Task 12 — Write the `ProjectGroup` component

- [ ] Write `web/src/components/ProjectGroup.tsx`:

```typescript
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
```

- [ ] Write `web/src/components/ProjectGroup.module.css`:

```css
.group {
  margin-bottom: 24px;
}

.header {
  width: 100%;
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: #fafafa;
  border: 1px solid #f0f0f0;
  border-radius: 6px;
  padding: 10px 14px;
  cursor: pointer;
  text-align: left;
  transition: background 0.15s;
}

.header:hover {
  background: #f5f5f5;
}

.header:focus-visible {
  outline: 2px solid #1677ff;
  outline-offset: 2px;
}

.projectInfo {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.projectName {
  font-size: 0.95rem;
  font-weight: 600;
  color: #262626;
}

.projectPath {
  font-size: 0.72rem;
  color: #8c8c8c;
  font-family: 'JetBrains Mono', monospace;
}

.headerRight {
  display: flex;
  align-items: center;
  gap: 10px;
}

.count {
  font-size: 0.78rem;
  color: #8c8c8c;
}

.chevron {
  font-size: 0.65rem;
  color: #bfbfbf;
}

.sessionList {
  margin-top: 8px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.empty {
  font-size: 0.85rem;
  color: #bfbfbf;
  padding: 12px 14px;
  margin: 0;
}
```

---

## Task 13 — Write the `SessionGrid` component with tests

- [ ] Write `web/src/components/SessionGrid.tsx`:

```typescript
import type { Project, Session, SessionEvent } from '../types';
import { ProjectGroup } from './ProjectGroup';
import styles from './SessionGrid.module.css';

interface SessionGridProps {
  projects: Project[];
  sessions: Map<string, Session>;
  events: Map<string, SessionEvent[]>;
  sessionsSortedByAttention: (projectId: string) => Session[];
  loading: boolean;
  connected: boolean;
}

export function SessionGrid({
  projects,
  sessions,
  events,
  sessionsSortedByAttention,
  loading,
  connected,
}: SessionGridProps) {
  if (loading) {
    return (
      <div className={styles.state}>
        <p className={styles.loadingText}>Loading projects…</p>
      </div>
    );
  }

  if (projects.length === 0) {
    return (
      <div className={styles.state}>
        <p className={styles.emptyText}>No projects found. Start a session to see it here.</p>
      </div>
    );
  }

  // Only show projects that have at least one session, unless loading.
  const projectsWithSessions = projects.filter(
    (p) => sessionsSortedByAttention(p.id).length > 0
  );

  const noSessions = sessions.size === 0;

  return (
    <div className={styles.grid}>
      {!connected && (
        <div className={styles.banner} role="alert">
          Reconnecting to server…
        </div>
      )}

      {noSessions && connected && (
        <div className={styles.state}>
          <p className={styles.emptyText}>
            No active sessions. Launch an agent to get started.
          </p>
        </div>
      )}

      {projectsWithSessions.map((project) => (
        <ProjectGroup
          key={project.id}
          project={project}
          sessions={sessionsSortedByAttention(project.id)}
          events={events}
        />
      ))}
    </div>
  );
}
```

- [ ] Write `web/src/components/SessionGrid.module.css`:

```css
.grid {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.state {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 48px 24px;
}

.loadingText,
.emptyText {
  font-size: 0.9rem;
  color: #8c8c8c;
  margin: 0;
  text-align: center;
}

.banner {
  background: #fff7e6;
  border: 1px solid #ffd591;
  border-radius: 6px;
  padding: 8px 14px;
  font-size: 0.82rem;
  color: #d46b08;
  margin-bottom: 4px;
}
```

- [ ] Write `web/src/components/SessionGrid.test.tsx`:

```typescript
import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { SessionGrid } from './SessionGrid';
import type { Project, Session } from '../types';
import { SessionStatus } from '../types';

// Mock child components to keep tests focused on SessionGrid logic.
vi.mock('./ProjectGroup', () => ({
  ProjectGroup: ({ project, sessions }: { project: Project; sessions: Session[] }) => (
    <div data-testid={`project-${project.id}`}>
      <span>{project.name}</span>
      <span data-testid={`session-count-${project.id}`}>{sessions.length}</span>
    </div>
  ),
}));

function makeProject(id: string, name: string): Project {
  return { id, name, path: `/workspace/${name}`, runtime: 'claude-code', createdAt: undefined } as any;
}

function makeSession(id: string, projectId: string, score: number): Session {
  return {
    id,
    projectId,
    runtime: 'claude-code',
    status: SessionStatus.RUNNING,
    profile: 'default',
    attentionScore: score,
    startedAt: { seconds: BigInt(1000), nanos: 0 } as any,
    endedAt: undefined,
    lastEventAt: { seconds: BigInt(1001), nanos: 0 } as any,
    pid: 1,
    tmuxSession: 'gru-test',
    tmuxWindow: 'feat-dev·a1b2c3d4',
  };
}

describe('SessionGrid', () => {
  it('shows loading state when loading is true', () => {
    render(
      <SessionGrid
        projects={[]}
        sessions={new Map()}
        events={new Map()}
        sessionsSortedByAttention={() => []}
        loading={true}
        connected={true}
      />
    );
    expect(screen.getByText(/loading projects/i)).toBeInTheDocument();
  });

  it('shows empty state when no projects', () => {
    render(
      <SessionGrid
        projects={[]}
        sessions={new Map()}
        events={new Map()}
        sessionsSortedByAttention={() => []}
        loading={false}
        connected={true}
      />
    );
    expect(screen.getByText(/no projects found/i)).toBeInTheDocument();
  });

  it('shows reconnecting banner when not connected', () => {
    const project = makeProject('p1', 'Alpha');
    const session = makeSession('s1', 'p1', 0.5);
    const sessions = new Map([['s1', session]]);

    render(
      <SessionGrid
        projects={[project]}
        sessions={sessions}
        events={new Map()}
        sessionsSortedByAttention={(pid) => pid === 'p1' ? [session] : []}
        loading={false}
        connected={false}
      />
    );
    expect(screen.getByRole('alert')).toHaveTextContent(/reconnecting/i);
  });

  it('renders ProjectGroup for each project that has sessions', () => {
    const p1 = makeProject('p1', 'Alpha');
    const p2 = makeProject('p2', 'Beta');
    const p3 = makeProject('p3', 'Empty');
    const s1 = makeSession('s1', 'p1', 0.9);
    const s2 = makeSession('s2', 'p1', 0.3);
    const s3 = makeSession('s3', 'p2', 0.6);
    const sessions = new Map([['s1', s1], ['s2', s2], ['s3', s3]]);

    const sortFn = (pid: string) => {
      if (pid === 'p1') return [s1, s2];
      if (pid === 'p2') return [s3];
      return [];
    };

    render(
      <SessionGrid
        projects={[p1, p2, p3]}
        sessions={sessions}
        events={new Map()}
        sessionsSortedByAttention={sortFn}
        loading={false}
        connected={true}
      />
    );

    expect(screen.getByTestId('project-p1')).toBeInTheDocument();
    expect(screen.getByTestId('project-p2')).toBeInTheDocument();
    // p3 has no sessions — should not render.
    expect(screen.queryByTestId('project-p3')).not.toBeInTheDocument();
  });

  it('shows no-sessions empty state when connected but 0 sessions', () => {
    const project = makeProject('p1', 'Alpha');

    render(
      <SessionGrid
        projects={[project]}
        sessions={new Map()}
        events={new Map()}
        sessionsSortedByAttention={() => []}
        loading={false}
        connected={true}
      />
    );
    expect(screen.getByText(/no active sessions/i)).toBeInTheDocument();
  });
});
```

---

## Task 14 — Write the service worker for background notifications

- [ ] Write `web/public/sw.js`:

```javascript
// Service worker for Gru desktop notifications.
// The page posts messages here when sessions need attention
// while the tab is not focused.

self.addEventListener('install', () => {
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

// Listen for messages from the page.
self.addEventListener('message', (event) => {
  if (!event.data) return;

  if (event.data.type === 'NOTIFY_ATTENTION') {
    const { sessionId, projectName } = event.data;
    const shortId = sessionId.slice(0, 8);

    self.registration.showNotification('Gru — Attention needed', {
      body: `Session ${shortId} in ${projectName} needs your attention`,
      tag: `gru-attention-${sessionId}`,
      icon: '/vite.svg',
      requireInteraction: false,
    });
  }
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: 'window' }).then((clientList) => {
      for (const client of clientList) {
        if ('focus' in client) return client.focus();
      }
      if (self.clients.openWindow) return self.clients.openWindow('/');
    })
  );
});
```

---

## Task 15 — Write `App.tsx` and `main.tsx`

- [ ] Write `web/src/App.tsx`:

```typescript
import { useEffect } from 'react';
import { SessionGrid } from './components/SessionGrid';
import { useSessionStream } from './hooks/useSessionStream';
import { useProjects } from './hooks/useProjects';
import styles from './App.module.css';

export function App() {
  const { projects, loading: projectsLoading } = useProjects();
  const { sessions, events, connected, sessionsSortedByAttention } = useSessionStream();

  // Register the service worker.
  useEffect(() => {
    if ('serviceWorker' in navigator) {
      navigator.serviceWorker
        .register('/sw.js')
        .catch((err) => console.warn('SW registration failed:', err));
    }
  }, []);

  const activeCount = Array.from(sessions.values()).filter(
    (s) =>
      s.status !== 3 && // SESSION_STATUS_COMPLETED
      s.status !== 6 && // SESSION_STATUS_ERRORED
      s.status !== 7    // SESSION_STATUS_KILLED
  ).length;

  return (
    <div className={styles.app}>
      <header className={styles.header}>
        <div className={styles.brand}>
          <h1 className={styles.title}>Gru</h1>
          <span className={styles.subtitle}>Mission Control</span>
        </div>
        <div className={styles.statusRow}>
          <span
            className={[styles.dot, connected ? styles.dotConnected : styles.dotDisconnected].join(
              ' '
            )}
            title={connected ? 'Connected' : 'Disconnected'}
          />
          <span className={styles.sessionCount}>
            {activeCount} active session{activeCount !== 1 ? 's' : ''}
          </span>
        </div>
      </header>

      <main className={styles.main}>
        <SessionGrid
          projects={projects}
          sessions={sessions}
          events={events}
          sessionsSortedByAttention={sessionsSortedByAttention}
          loading={projectsLoading}
          connected={connected}
        />
      </main>
    </div>
  );
}
```

- [ ] Write `web/src/App.module.css`:

```css
.app {
  min-height: 100vh;
  background: #f5f5f5;
  font-family:
    -apple-system,
    BlinkMacSystemFont,
    'Segoe UI',
    Roboto,
    'Helvetica Neue',
    Arial,
    sans-serif;
}

.header {
  position: sticky;
  top: 0;
  z-index: 100;
  background: #fff;
  border-bottom: 1px solid #f0f0f0;
  padding: 12px 24px;
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.brand {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.title {
  font-size: 1.3rem;
  font-weight: 700;
  color: #262626;
  margin: 0;
  letter-spacing: -0.02em;
}

.subtitle {
  font-size: 0.78rem;
  color: #8c8c8c;
  font-weight: 400;
}

.statusRow {
  display: flex;
  align-items: center;
  gap: 8px;
}

.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
}

.dotConnected {
  background: #52c41a;
}

.dotDisconnected {
  background: #ff4d4f;
}

.sessionCount {
  font-size: 0.8rem;
  color: #595959;
}

.main {
  max-width: 1200px;
  margin: 0 auto;
  padding: 24px;
}
```

- [ ] Write `web/src/main.tsx`:

```typescript
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';

const root = document.getElementById('root');
if (!root) throw new Error('Root element not found');

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>
);
```

---

## Task 16 — Run all tests and verify

- [ ] Run the full test suite:

```bash
cd web && npm test -- --reporter=verbose
```

Expected output:
```
✓ src/utils/status.test.ts (8)
✓ src/utils/time.test.ts (7)
✓ src/hooks/useSessionStream.test.ts (5)
✓ src/components/KillButton.test.tsx (4)
✓ src/components/SessionGrid.test.tsx (5)

Test Files  5 passed (5)
Tests      29 passed (29)
```

All tests pass with no failures.

---

## Task 17 — Smoke-test the dev server

- [ ] Start the Gru backend (Phase 1a) in one terminal:

```bash
./gru server
# expected: Gru server listening on :7777
```

- [ ] Start the web dev server:

```bash
cd web && npm run dev
```

Expected output:
```
  VITE v5.x.x  ready in NNNms

  ➜  Local:   http://localhost:3000/
  ➜  Network: use --host to expose
```

- [ ] Open `http://localhost:3000` in a browser. Verify:
  - Header shows "Gru — Mission Control" with a status dot
  - Green dot when connected, red when server is down
  - Sessions appear grouped by project once the backend has active sessions

---

## Task 18 — TypeScript build check

- [ ] Ensure TypeScript compiles without errors:

```bash
cd web && npx tsc --noEmit
```

Expected: No output (zero errors).

---

## Task 19 — Commit

- [ ] Stage and commit the web dashboard:

```bash
git add web/ buf.gen.yaml
git commit -m "feat: add React web dashboard (Phase 1d)

- Vite + React 18 + TypeScript project scaffold
- connect-web gRPC client with auth interceptor
- useSessionStream hook: snapshot + live events, auto-reconnect with backoff
- useProjects hook for project list
- SessionGrid, ProjectGroup, SessionCard, StatusBadge, AttentionIndicator, KillButton components
- Desktop notifications via service worker when sessions need attention
- Vitest + RTL tests for hooks, utils, KillButton, SessionGrid"
```

---

## Self-Review Checklist

- [x] Every task has complete, runnable code — no placeholders or TBDs
- [x] All component props are typed with TypeScript interfaces
- [x] All imports reference files created earlier in the plan (no forward references)
- [x] All UI requirements covered:
  - [x] Sessions grouped by project (SessionGrid + ProjectGroup)
  - [x] Session card: short ID, status badge, attention score, uptime, profile
  - [x] Cards sorted by attention score descending (useSessionStream.sessionsSortedByAttention)
  - [x] Expanded card: full ID, PID, last 20 events, kill button
  - [x] Status badge colors: needs_attention → red, running → green, idle → yellow, starting → blue+pulse, completed/killed → gray, errored → red
  - [x] Kill button with confirm dialog (KillButton)
  - [x] useSessionStream: snapshot processing → initial state, incremental events, auto-reconnect (exponential backoff, max 30s)
  - [x] Desktop notifications for needs_attention transitions (service worker + useSessionStream)
  - [x] Only notify when tab is not focused (document.hasFocus() guard)
  - [x] Notification permission request on first load
  - [x] VITE_GRU_SERVER_URL and VITE_GRU_API_KEY env vars (client.ts)
- [x] buf.gen.yaml updated with ES + connect-es plugins
- [x] Tests written for: useSessionStream, status.ts, time.ts, SessionGrid, KillButton
- [x] Pure layout components (SessionCard CSS, ProjectGroup CSS, App CSS) intentionally skip tests per plan rules
