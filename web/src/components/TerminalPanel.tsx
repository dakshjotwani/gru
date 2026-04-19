import { useEffect, useRef } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import type { Session } from '../types';
import styles from './TerminalPanel.module.css';
import '@xterm/xterm/css/xterm.css';

const serverUrl = import.meta.env.VITE_GRU_SERVER_URL ?? 'http://localhost:7777';
const apiKey = import.meta.env.VITE_GRU_API_KEY ?? '';

// iPad Safari silently stalls WebSocket connections when a previous connection
// to the same endpoint is still closing (common when cycling between sessions).
// The stalled socket stays in CONNECTING indefinitely — no error, no close, just
// silence.  We work around this with a connect timeout: if the socket hasn't
// reached OPEN within WS_CONNECT_TIMEOUT_MS we tear it down and retry, up to
// WS_MAX_RETRIES times.  1 second is enough for the old connection to finish
// closing; the retry connects immediately after.  250ms is generous for a
// localhost connection (typically <5ms) but safe under load.
const WS_CONNECT_TIMEOUT_MS = 250;
const WS_MAX_RETRIES = 3;

interface TerminalPanelProps {
  session: Session;
  /** Parent sets this ref to call focus() on the terminal from outside. */
  focusRef?: React.RefObject<(() => void) | null>;
}

export function TerminalPanel({ session, focusRef }: TerminalPanelProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    let disposed = false;
    let term: Terminal | null = null;
    let fitAddon: FitAddon | null = null;
    let ws: WebSocket | null = null;
    let connectTimer: ReturnType<typeof setTimeout> | undefined;

    const termTheme = {
      background: '#0d1117',
      foreground: '#c9d1d9',
      cursor: '#58a6ff',
      cursorAccent: '#0d1117',
      selectionBackground: '#264f78',
      black: '#0d1117',
      brightBlack: '#6e7681',
      red: '#ff7b72',
      brightRed: '#ffa198',
      green: '#3fb950',
      brightGreen: '#56d364',
      yellow: '#d29922',
      brightYellow: '#e3b341',
      blue: '#58a6ff',
      brightBlue: '#79c0ff',
      magenta: '#bc8cff',
      brightMagenta: '#d2a8ff',
      cyan: '#39c5cf',
      brightCyan: '#56d4dd',
      white: '#b1bac4',
      brightWhite: '#f0f6fc',
    };

    // ── Terminal setup (deferred until container has real dimensions) ──

    const initTerminal = () => {
      term = new Terminal({
        fontFamily: '"JetBrains Mono", "Fira Code", "SF Mono", monospace',
        fontSize: 13,
        lineHeight: 1.4,
        cursorBlink: true,
        theme: termTheme,
      });

      fitAddon = new FitAddon();
      term.loadAddon(fitAddon);
      term.open(container);
      fitAddon.fit();
      term.focus();

      // Claim keys the browser/React-app would otherwise steal from the
      // terminal: Ctrl+C (browser copies selection), Tab (focus move),
      // Escape (app deselects minion), Ctrl+Z/D, arrows.
      // Ctrl+\ is deliberately NOT claimed — App.tsx uses it as a
      // global "toggle sidebar nav mode" shortcut.
      term.attachCustomKeyEventHandler((e) => {
        if (e.type !== 'keydown') return true;
        const { key, ctrlKey, metaKey, altKey } = e;
        const isClaimed =
          key === 'Tab' ||
          key === 'Escape' ||
          key === 'ArrowUp' || key === 'ArrowDown' ||
          key === 'ArrowLeft' || key === 'ArrowRight' ||
          (ctrlKey && !metaKey && !altKey &&
            (key === 'c' || key === 'd' || key === 'z'));
        if (isClaimed) {
          e.preventDefault();
          e.stopPropagation();
        }
        return true;
      });

      if (focusRef) focusRef.current = () => term?.focus();
    };

    // ── WebSocket with connect timeout + retry ──
    // iPad Safari can silently stall WebSocket connections during rapid
    // open/close cycles. A connect timeout + retry makes this recoverable.

    const connectWs = (attempt: number) => {
      if (disposed || !term) return;

      const wsBase = serverUrl.replace(/^http/, 'ws');
      const socket = new WebSocket(
        `${wsBase}/terminal/${session.id}?token=${encodeURIComponent(apiKey)}`
      );
      socket.binaryType = 'arraybuffer';
      ws = socket;

      // Guard: if the socket hasn't opened within the timeout, close it and
      // retry.  This handles the iPad Safari stall case.
      connectTimer = setTimeout(() => {
        if (socket.readyState === WebSocket.CONNECTING) {
          socket.close();
          if (attempt < WS_MAX_RETRIES && !disposed) {
            term?.write(`\r\n\x1b[2m[reconnecting…]\x1b[0m\r\n`);
            connectWs(attempt + 1);
          } else {
            term?.write(`\r\n\x1b[31m[connection timed out]\x1b[0m\r\n`);
          }
        }
      }, WS_CONNECT_TIMEOUT_MS);

      socket.onopen = () => {
        clearTimeout(connectTimer);
        if (fitAddon) {
          const dims = fitAddon.proposeDimensions();
          if (dims) {
            socket.send(JSON.stringify({ type: 'resize', cols: dims.cols, rows: dims.rows }));
          }
        }
      };

      socket.onmessage = (e) => {
        if (e.data instanceof ArrayBuffer) {
          term?.write(new Uint8Array(e.data));
        } else {
          term?.write(String(e.data));
        }
      };

      socket.onclose = () => {
        clearTimeout(connectTimer);
        if (!disposed) {
          term?.write('\r\n\x1b[2m[connection closed]\x1b[0m\r\n');
        }
      };

      socket.onerror = () => {
        clearTimeout(connectTimer);
        if (!disposed) {
          // Don't write error here — onclose always fires after onerror, so
          // the retry logic in the timeout handler or the close message covers it.
        }
      };

      term!.onData((data) => {
        if (socket.readyState === WebSocket.OPEN) {
          socket.send(new TextEncoder().encode(data));
        }
      });

      term!.onResize(({ cols, rows }) => {
        if (socket.readyState === WebSocket.OPEN) {
          socket.send(JSON.stringify({ type: 'resize', cols, rows }));
        }
      });
    };

    // ── Boot: wait for container dimensions, then init terminal + connect ──

    const boot = () => {
      if (disposed || term) return;
      initTerminal();
      connectWs(1);
    };

    const observer = new ResizeObserver(() => {
      if (!term) {
        const { width, height } = container.getBoundingClientRect();
        if (width > 0 && height > 0) boot();
      } else {
        try { fitAddon?.fit(); } catch { /* unmount race */ }
      }
    });
    observer.observe(container);

    // Eagerly boot if the container already has dimensions.
    const { width, height } = container.getBoundingClientRect();
    if (width > 0 && height > 0) boot();

    return () => {
      disposed = true;
      clearTimeout(connectTimer);
      if (focusRef) focusRef.current = null;
      observer.disconnect();
      ws?.close();
      term?.dispose();
    };
  }, [session.id]);

  return (
    <div className={styles.panel}>
      <div className={styles.titleBar}>
        <span className={styles.sessionName}>{session.name || session.id.slice(0, 8)}</span>
        {session.tmuxSession && (
          <span className={styles.tmuxTarget}>{session.tmuxSession}</span>
        )}
      </div>
      <div ref={containerRef} className={styles.terminal} data-gru-terminal />
    </div>
  );
}
