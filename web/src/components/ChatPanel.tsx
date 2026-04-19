// Chat-style renderer for a single session's event stream.
//
// Same gRPC SubscribeEvents feed as the terminal — different
// presentation. The hook already maintains the last 20 events per
// session in a Map; we slice out the selected session, decode
// payloads to get message / tool-name context, and render each as a
// bubble per the spec's event→rendering table.
//
// Input bar at the bottom calls the SendInput RPC (existing), which
// shells out to `tmux send-keys` on the server. On iPhone, Enter
// inserts a newline and the send button submits. On iPad with a
// Magic Keyboard, Enter submits and Shift+Enter inserts a newline,
// matching iMessage/Slack/Claude conventions.

import { useEffect, useMemo, useRef, useState } from 'react';
import { gruClient } from '../client';
import type { Session, SessionEvent } from '../types';
import styles from './ChatPanel.module.css';

interface ChatPanelProps {
  session: Session;
  events: SessionEvent[];
}

export function ChatPanel({ session, events }: ChatPanelProps) {
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);

  // Auto-scroll to bottom on new event if we're already near-bottom.
  // Users who scrolled up stay put.
  const lastEventId = events.length > 0 ? events[events.length - 1].id : '';
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
    if (nearBottom) {
      el.scrollTop = el.scrollHeight;
    }
  }, [lastEventId]);

  // Decode payloads once per render cycle.
  const bubbles = useMemo(() => events.map(eventToBubble), [events]);

  const send = async (text: string) => {
    const clean = text.trim();
    if (!clean || sending) return;
    setSending(true);
    try {
      await gruClient.sendInput({ sessionId: session.id, text: clean });
      setDraft('');
    } catch (err) {
      console.warn('sendInput failed:', err);
    } finally {
      setSending(false);
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // Enter submits on desktop / iPad-with-keyboard. Shift+Enter inserts newline.
    // On touch-only iPhone the on-screen keyboard doesn't emit Enter for send
    // (it inserts newline); users tap the send button.
    if (e.key === 'Enter' && !e.shiftKey && !isTouchOnly()) {
      e.preventDefault();
      void send(draft);
    }
  };

  return (
    <div className={styles.panel}>
      <div className={styles.titleBar}>
        <span className={styles.sessionName}>{session.name || session.id.slice(0, 8)}</span>
        <span className={styles.status}>{humanStatus(session.status)}</span>
      </div>

      <div ref={scrollRef} className={styles.feed}>
        {bubbles.length === 0 && (
          <div className={styles.empty}>no events yet — the session is just starting</div>
        )}
        {bubbles.map((b) => (
          <Bubble key={b.id} bubble={b} />
        ))}
      </div>

      <form
        className={styles.inputRow}
        onSubmit={(e) => {
          e.preventDefault();
          void send(draft);
        }}
      >
        <textarea
          className={styles.input}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="reply…"
          rows={1}
          disabled={sending}
        />
        <button
          type="submit"
          className={styles.send}
          disabled={sending || !draft.trim()}
          aria-label="send"
        >
          Send
        </button>
      </form>
    </div>
  );
}

function Bubble({ bubble }: { bubble: BubbleData }) {
  if (bubble.kind === 'divider') {
    return <div className={styles.divider}>{bubble.text}</div>;
  }
  const className = [
    styles.bubble,
    bubble.kind === 'user' ? styles.bubbleUser : '',
    bubble.kind === 'assistant' ? styles.bubbleAssistant : '',
    bubble.kind === 'system' ? styles.bubbleSystem : '',
    bubble.kind === 'attention' ? styles.bubbleAttention : '',
    bubble.kind === 'tool' ? styles.bubbleTool : '',
    bubble.kind === 'toolError' ? styles.bubbleToolError : '',
  ]
    .filter(Boolean)
    .join(' ');
  return (
    <div className={className}>
      {bubble.label && <div className={styles.bubbleLabel}>{bubble.label}</div>}
      <div className={styles.bubbleBody}>{bubble.text}</div>
    </div>
  );
}

type BubbleKind =
  | 'user'
  | 'assistant'
  | 'tool'
  | 'toolError'
  | 'system'
  | 'attention'
  | 'divider';

interface BubbleData {
  id: string;
  kind: BubbleKind;
  label?: string;
  text: string;
}

// eventToBubble renders one GruEvent as a BubbleData. Mapping comes
// from the spec's "event → bubble" table.
function eventToBubble(event: SessionEvent): BubbleData {
  const payload = decodePayload(event.payload);
  switch (event.type) {
    case 'user.prompt': {
      const prompt = typeof payload.prompt === 'string' ? payload.prompt : '';
      return { id: event.id, kind: 'user', text: prompt || '(empty prompt)' };
    }
    case 'session.idle': {
      const msg = messageFromStop(payload);
      return { id: event.id, kind: 'assistant', text: msg || '(turn complete)' };
    }
    case 'tool.pre': {
      const name = typeof payload.tool_name === 'string' ? payload.tool_name : 'tool';
      const summary = summarizeToolInput(name, payload.tool_input);
      return { id: event.id, kind: 'tool', label: '🔧 ' + name, text: summary };
    }
    case 'tool.post': {
      const name = typeof payload.tool_name === 'string' ? payload.tool_name : 'tool';
      return { id: event.id, kind: 'tool', label: name + ' →', text: summarizeToolResponse(payload.tool_response) };
    }
    case 'tool.error': {
      const name = typeof payload.tool_name === 'string' ? payload.tool_name : 'tool';
      return { id: event.id, kind: 'toolError', label: name + ' ✕', text: summarizeToolResponse(payload.tool_response) };
    }
    case 'notification.needs_attention': {
      const msg = typeof payload.message === 'string' ? payload.message : 'needs your input';
      return { id: event.id, kind: 'attention', label: 'Needs input', text: msg };
    }
    case 'notification': {
      const msg = typeof payload.message === 'string' ? payload.message : '(notification)';
      return { id: event.id, kind: 'system', text: msg };
    }
    case 'session.start':
      return { id: event.id, kind: 'divider', text: `started · ${formatTime(event.timestamp)}` };
    case 'session.end':
      return { id: event.id, kind: 'divider', text: `ended · ${formatTime(event.timestamp)}` };
    case 'session.crash':
      return { id: event.id, kind: 'divider', text: `crashed · ${formatTime(event.timestamp)}` };
    case 'subagent.start':
      return { id: event.id, kind: 'divider', text: '→ subagent started' };
    case 'subagent.end':
      return { id: event.id, kind: 'divider', text: '← subagent finished' };
    default:
      return { id: event.id, kind: 'system', text: event.type };
  }
}

function decodePayload(payload: Uint8Array | string | undefined): Record<string, unknown> {
  if (!payload) return {};
  try {
    const str = typeof payload === 'string' ? payload : new TextDecoder().decode(payload);
    return JSON.parse(str);
  } catch {
    return {};
  }
}

function messageFromStop(payload: Record<string, unknown>): string {
  // Claude Code's Stop hook carries a `transcript_path` but not the
  // final text directly. Fall back to any `message` field the
  // normalizer or journal layer may have added.
  if (typeof payload.message === 'string') return payload.message;
  return '';
}

function summarizeToolInput(toolName: string, input: unknown): string {
  if (!input || typeof input !== 'object') return '';
  const obj = input as Record<string, unknown>;
  if (toolName === 'Bash' && typeof obj.command === 'string') {
    return truncate(obj.command, 240);
  }
  if ((toolName === 'Read' || toolName === 'Edit' || toolName === 'Write') && typeof obj.file_path === 'string') {
    return truncate(obj.file_path, 240);
  }
  if (toolName === 'Grep' && typeof obj.pattern === 'string') {
    return truncate(String(obj.pattern), 240);
  }
  try {
    return truncate(JSON.stringify(input), 240);
  } catch {
    return '';
  }
}

function summarizeToolResponse(resp: unknown): string {
  if (!resp) return '';
  if (typeof resp === 'string') return truncate(resp, 1600);
  try {
    return truncate(JSON.stringify(resp), 1600);
  } catch {
    return '';
  }
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, n - 1) + '…';
}

function formatTime(ts: unknown): string {
  if (!ts) return '';
  const d = typeof ts === 'string' ? new Date(ts) : ts instanceof Date ? ts : null;
  if (!d || isNaN(d.getTime())) return '';
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function humanStatus(status: unknown): string {
  if (typeof status !== 'number') return '';
  // Enum ordinals match proto SessionStatus.
  switch (status) {
    case 1: return 'starting';
    case 2: return 'running';
    case 3: return 'idle';
    case 4: return 'needs attention';
    case 5: return 'completed';
    case 6: return 'errored';
    case 7: return 'killed';
    default: return '';
  }
}

function isTouchOnly(): boolean {
  // Heuristic: touch points + no coarse-grained pointer (mouse).
  // Both needed because iPad with Magic Keyboard still has touch points.
  return 'ontouchstart' in window && !window.matchMedia('(hover: hover)').matches;
}
