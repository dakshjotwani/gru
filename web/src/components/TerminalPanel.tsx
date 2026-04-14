import { useEffect, useRef } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import type { Session } from '../types';
import styles from './TerminalPanel.module.css';
import '@xterm/xterm/css/xterm.css';

const serverUrl = import.meta.env.VITE_GRU_SERVER_URL ?? 'http://localhost:7777';
const apiKey = import.meta.env.VITE_GRU_API_KEY ?? '';

interface TerminalPanelProps {
  session: Session;
  /** Parent sets this ref to call focus() on the terminal from outside. */
  focusRef?: React.RefObject<(() => void) | null>;
}

export function TerminalPanel({ session, focusRef }: TerminalPanelProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    const term = new Terminal({
      fontFamily: '"JetBrains Mono", "Fira Code", "SF Mono", monospace',
      fontSize: 13,
      lineHeight: 1.4,
      cursorBlink: true,
      theme: {
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
      },
    });

    const fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(containerRef.current);
    fitAddon.fit();
    term.focus();

    // Expose focus fn to parent so it can pull focus back after sidebar navigation.
    if (focusRef) focusRef.current = () => term.focus();

    // Build WebSocket URL: swap http(s):// for ws(s)://
    const wsBase = serverUrl.replace(/^http/, 'ws');
    const ws = new WebSocket(
      `${wsBase}/terminal/${session.id}?token=${encodeURIComponent(apiKey)}`
    );
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      const dims = fitAddon.proposeDimensions();
      if (dims) {
        ws.send(JSON.stringify({ type: 'resize', cols: dims.cols, rows: dims.rows }));
      }
    };

    ws.onmessage = (e) => {
      if (e.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(e.data));
      } else {
        term.write(String(e.data));
      }
    };

    ws.onclose = () => {
      term.write('\r\n\x1b[2m[connection closed]\x1b[0m\r\n');
    };

    ws.onerror = () => {
      term.write('\r\n\x1b[31m[connection error]\x1b[0m\r\n');
    };

    // Terminal input → WebSocket (send as binary to distinguish from control frames)
    term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(new TextEncoder().encode(data));
      }
    });

    // Terminal resize → send JSON control frame
    term.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols, rows }));
      }
    });

    // Container resize → refit terminal
    const observer = new ResizeObserver(() => {
      try {
        fitAddon.fit();
      } catch {
        // ignore resize errors during unmount
      }
    });
    observer.observe(containerRef.current);

    return () => {
      if (focusRef) focusRef.current = null;
      observer.disconnect();
      ws.close();
      term.dispose();
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
      <div ref={containerRef} className={styles.terminal} />
    </div>
  );
}
