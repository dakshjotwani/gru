import { useEffect, useRef } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { Unicode11Addon } from '@xterm/addon-unicode11';
import { WebLinksAddon } from '@xterm/addon-web-links';
import type { Session } from '../types';
import { resolveWebSocketUrl } from '../utils/serverUrl';
import styles from './TerminalPanel.module.css';
import '@xterm/xterm/css/xterm.css';

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

// Mobile PWA resume: iOS/Android aggressively suspend backgrounded tabs,
// which drops the WebSocket. In-flight retry timers can also be paused or
// throttled while suspended, so on resume we both (a) let any scheduled
// backoff continue and (b) actively force an immediate reconnect on
// visibilitychange / pageshow / focus / online events.
const WS_RECONNECT_INITIAL_BACKOFF_MS = 500;
const WS_RECONNECT_MAX_BACKOFF_MS = 8000;

interface TerminalPanelProps {
  session: Session;
  /** Parent sets this ref to call focus() on the terminal from outside. */
  focusRef?: React.RefObject<(() => void) | null>;
  fullscreen?: boolean;
  onToggleFullscreen?: () => void;
}

// hasCoarsePointer is true on phones / tablets without an attached mouse —
// i.e. the device's primary input is a finger, not a cursor. Used to decide
// whether links should activate on a plain tap (no Ctrl/Cmd modifier).
function hasCoarsePointer(): boolean {
  if (typeof window === 'undefined' || !window.matchMedia) return false;
  return window.matchMedia('(pointer: coarse)').matches;
}

export function TerminalPanel({ session, focusRef, fullscreen, onToggleFullscreen }: TerminalPanelProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    let disposed = false;
    let term: Terminal | null = null;
    let fitAddon: FitAddon | null = null;
    let ws: WebSocket | null = null;
    let connectTimer: ReturnType<typeof setTimeout> | undefined;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    let touchListenerCleanup: (() => void) | undefined;
    let reconnectBackoffMs = WS_RECONNECT_INITIAL_BACKOFF_MS;
    // Once the socket has opened at least once, any subsequent close is
    // treated as a transient drop (backoff reconnect). Before that, we
    // preserve the original "give up after WS_MAX_RETRIES initial attempts"
    // behavior so dead sessions don't retry forever.
    let hasEverOpened = false;

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
        // Larger scrollback so mobile users have something to swipe back
        // through. Default (1000) is fine on desktop but feels short when
        // the terminal is the primary surface.
        scrollback: 5000,
        // Unicode11Addon and term.unicode.* are flagged as proposed API in
        // xterm.js — accessing them without this opt-in throws during
        // addon activation, which propagates out of React's commit phase
        // and unmounts the whole app (blank #root).
        allowProposedApi: true,
      });

      fitAddon = new FitAddon();
      term.loadAddon(fitAddon);

      // Unicode 11 width tables — without this xterm uses Unicode 6
      // wcwidth, which sizes newer emoji and nerd-font glyphs as one
      // cell when they should be two. Manifests as misaligned columns
      // and overlapping glyphs when Claude Code emits powerline / nerd-
      // font output. unicodeVersion must be set after loadAddon().
      term.loadAddon(new Unicode11Addon());
      term.unicode.activeVersion = '11';

      // Web-links addon — detects URLs in the buffer and registers a link
      // provider with xterm. On desktop, xterm's link service activates the
      // link on cmd/ctrl + click (default WebLinksAddon handler opens in
      // a new tab). On touch devices xterm's link hover state never gets
      // set (no mousemove), so the addon alone doesn't give tap-to-open —
      // the touch handler below uses the same regex to find URLs at the
      // tap position and opens them on a quick tap.
      const linksAddon = new WebLinksAddon((_event, uri) => {
        window.open(uri, '_blank', 'noopener,noreferrer');
      });
      term.loadAddon(linksAddon);

      term.open(container);
      fitAddon.fit();
      term.focus();

      // Tap-to-open links on touchscreens. xterm's link activation
      // requires the cmd/ctrl modifier on desktop and the link to be
      // in a "hovered" state — neither of which works on touch (no
      // mousemove → no hover; no modifier keys). So when the device
      // has a coarse pointer (no mouse), we listen for short-tap
      // gestures, locate the cell at the tap point, scan the buffer
      // line for a URL covering that cell, and open it ourselves.
      // Desktop is left alone so the cmd/ctrl-click flow still works.
      if (hasCoarsePointer()) {
        let touchStartX = 0;
        let touchStartY = 0;
        let touchStartTime = 0;
        let touchCount = 0;

        const onTouchStart = (e: TouchEvent) => {
          touchCount = e.touches.length;
          if (touchCount !== 1) return;
          touchStartX = e.touches[0].clientX;
          touchStartY = e.touches[0].clientY;
          touchStartTime = Date.now();
        };

        const onTouchEnd = (e: TouchEvent) => {
          if (touchCount !== 1 || e.changedTouches.length !== 1) return;
          const t = e.changedTouches[0];
          const dx = t.clientX - touchStartX;
          const dy = t.clientY - touchStartY;
          const dt = Date.now() - touchStartTime;
          // Tap = brief, no significant drag. Longer holds let iOS open
          // its native context menu (copy / look up); drags are scroll.
          if (dt > 350 || Math.abs(dx) > 8 || Math.abs(dy) > 8) return;

          const url = findUrlAt(t.clientX, t.clientY);
          if (url) {
            e.preventDefault();
            window.open(url, '_blank', 'noopener,noreferrer');
          }
        };

        const findUrlAt = (clientX: number, clientY: number): string | null => {
          if (!term) return null;
          const screen = container.querySelector('.xterm-screen') as HTMLElement | null;
          if (!screen) return null;
          const rect = screen.getBoundingClientRect();
          if (clientX < rect.left || clientX > rect.right) return null;
          if (clientY < rect.top || clientY > rect.bottom) return null;
          const cellWidth = rect.width / term.cols;
          const cellHeight = rect.height / term.rows;
          if (cellWidth <= 0 || cellHeight <= 0) return null;
          const col = Math.floor((clientX - rect.left) / cellWidth);
          const visibleRow = Math.floor((clientY - rect.top) / cellHeight);

          // Match the regex used by @xterm/addon-web-links so detected
          // URLs are exactly the same set as the underline targets.
          const urlRegex = /(https?|HTTPS?):\/\/[^\s"'!*(){}|\\^<>`]*[^\s"':,.!?{}|\\^~[\]`()<>]/g;

          const buffer = term.buffer.active;
          const baseRow = buffer.viewportY + visibleRow;
          // Walk back through wrapped lines to get the full logical line,
          // then forward through any continuations.  This matches what
          // the addon does internally so URLs that wrap across the screen
          // boundary still resolve.
          let startRow = baseRow;
          while (startRow > 0) {
            const line = buffer.getLine(startRow);
            if (!line || !line.isWrapped) break;
            startRow--;
          }
          let text = '';
          // The cell index inside `text` that corresponds to the tap.
          let tapIdx = -1;
          let row = startRow;
          while (true) {
            const line = buffer.getLine(row);
            if (!line) break;
            const lineText = line.translateToString(true);
            if (row === baseRow) tapIdx = text.length + col;
            text += lineText;
            const next = buffer.getLine(row + 1);
            if (!next || !next.isWrapped) break;
            row++;
          }
          if (tapIdx < 0) return null;

          let m: RegExpExecArray | null;
          while ((m = urlRegex.exec(text)) !== null) {
            const start = m.index;
            const end = start + m[0].length;
            if (tapIdx >= start && tapIdx < end) return m[0];
          }
          return null;
        };

        container.addEventListener('touchstart', onTouchStart, { passive: true });
        container.addEventListener('touchend', onTouchEnd, { passive: false });

        touchListenerCleanup = () => {
          container.removeEventListener('touchstart', onTouchStart);
          container.removeEventListener('touchend', onTouchEnd);
        };
      }

      // Claim keys the browser/React-app would otherwise steal from the
      // terminal: Ctrl+C (browser copies selection), Tab (focus move),
      // Escape (app deselects minion), Ctrl+Z/D, arrows.
      // Ctrl+\ is deliberately NOT claimed — App.tsx uses it as a
      // global "toggle sidebar nav mode" shortcut.
      term.attachCustomKeyEventHandler((e) => {
        if (e.type !== 'keydown') return true;
        const { key, ctrlKey, metaKey, altKey, shiftKey } = e;

        // Ctrl+Shift+F is the global fullscreen toggle — let it bubble
        // up to App.tsx (which captures at window level).
        if (ctrlKey && shiftKey && !altKey && !metaKey && key.toLowerCase() === 'f') {
          return false;
        }

        // Explicit control characters. xterm's default handling is
        // unreliable across iPad/iPhone Safari + Bluetooth keyboards
        // (Ctrl+C in particular has been observed to send '\r' on iPad
        // due to some IME/keyboard-layer interaction). We send the raw
        // bytes ourselves and tell xterm not to also handle them, which
        // also prevents the browser's "copy selection" default.
        if (ctrlKey && !metaKey && !altKey && !shiftKey) {
          let byte: string | null = null;
          // Lowercase the letter — Safari sometimes reports key='C' when
          // Caps Lock is engaged.
          const k = key.length === 1 ? key.toLowerCase() : key;
          if (k === 'c') byte = '\x03';        // SIGINT
          else if (k === 'd') byte = '\x04';   // EOF
          else if (k === 'z') byte = '\x1a';   // SIGTSTP
          if (byte !== null) {
            e.preventDefault();
            e.stopPropagation();
            if (ws && ws.readyState === WebSocket.OPEN) {
              ws.send(new TextEncoder().encode(byte));
            }
            return false;
          }
        }

        const isClaimed =
          key === 'Tab' ||
          key === 'Escape' ||
          key === 'ArrowUp' || key === 'ArrowDown' ||
          key === 'ArrowLeft' || key === 'ArrowRight';
        if (isClaimed) {
          e.preventDefault();
          e.stopPropagation();
        }
        return true;
      });

      if (focusRef) focusRef.current = () => term?.focus();

      // Attach input/resize forwarding ONCE. These close over `ws`, which is
      // reassigned on every (re)connect — so keystrokes always flow to the
      // currently-live socket. If we re-added these per connectWs() call
      // (as an earlier version did), each reconnect would stack another
      // listener and the terminal would echo each keystroke N times.
      term.onData((data) => {
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(new TextEncoder().encode(data));
        }
      });

      term.onResize(({ cols, rows }) => {
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(JSON.stringify({ type: 'resize', cols, rows }));
        }
      });
    };

    // ── WebSocket with connect timeout + retry ──
    // iPad Safari can silently stall WebSocket connections during rapid
    // open/close cycles. A connect timeout + retry makes this recoverable.

    const scheduleReconnect = (delayMs: number) => {
      if (disposed) return;
      clearTimeout(reconnectTimer);
      reconnectTimer = setTimeout(() => {
        if (disposed) return;
        reconnectBackoffMs = Math.min(
          reconnectBackoffMs * 2,
          WS_RECONNECT_MAX_BACKOFF_MS,
        );
        connectWs(1);
      }, delayMs);
    };

    const connectWs = (attempt: number) => {
      if (disposed || !term) return;

      clearTimeout(connectTimer);
      clearTimeout(reconnectTimer);

      const socket = new WebSocket(`${resolveWebSocketUrl()}/terminal/${session.id}`);
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
          } else if (!hasEverOpened) {
            term?.write(`\r\n\x1b[31m[connection timed out]\x1b[0m\r\n`);
          }
          // If hasEverOpened is true, the onclose handler below will take
          // over and schedule a backoff reconnect.
        }
      }, WS_CONNECT_TIMEOUT_MS);

      socket.onopen = () => {
        clearTimeout(connectTimer);
        hasEverOpened = true;
        reconnectBackoffMs = WS_RECONNECT_INITIAL_BACKOFF_MS;
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
        if (disposed) return;
        // Stale close from a socket we've already replaced (e.g. the
        // connect-timeout path swapped in a new attempt before this one
        // finished closing). Ignore — the live socket owns the retry policy.
        if (ws !== socket) return;
        if (hasEverOpened) {
          // Mid-session drop — typically PWA suspend/resume on iOS/Android,
          // or a transient network blip. Schedule a backoff reconnect; a
          // visibilitychange / pageshow / focus / online event will short-
          // circuit the backoff and retry immediately.
          term?.write(`\r\n\x1b[2m[reconnecting…]\x1b[0m\r\n`);
          scheduleReconnect(reconnectBackoffMs);
        } else if (attempt >= WS_MAX_RETRIES) {
          // Never opened and exhausted initial attempts — give up so dead
          // sessions don't reconnect forever.
          term?.write('\r\n\x1b[31m[connection closed]\x1b[0m\r\n');
        }
      };

      socket.onerror = () => {
        clearTimeout(connectTimer);
        // onclose always fires after onerror — let it make the retry call.
      };
    };

    // Force an immediate reconnect if the socket is not currently healthy.
    // Called from resume-style events below.
    const reconnectNowIfNeeded = () => {
      if (disposed || !term) return;
      if (!hasEverOpened) return; // preserve initial-attempt semantics
      if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;
      clearTimeout(reconnectTimer);
      reconnectBackoffMs = WS_RECONNECT_INITIAL_BACKOFF_MS;
      connectWs(1);
    };

    const onVisibilityChange = () => {
      if (document.visibilityState === 'visible') reconnectNowIfNeeded();
    };
    const onPageShow = () => reconnectNowIfNeeded();
    const onFocus = () => reconnectNowIfNeeded();
    const onOnline = () => reconnectNowIfNeeded();

    document.addEventListener('visibilitychange', onVisibilityChange);
    window.addEventListener('pageshow', onPageShow);
    window.addEventListener('focus', onFocus);
    window.addEventListener('online', onOnline);

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
      clearTimeout(reconnectTimer);
      document.removeEventListener('visibilitychange', onVisibilityChange);
      window.removeEventListener('pageshow', onPageShow);
      window.removeEventListener('focus', onFocus);
      window.removeEventListener('online', onOnline);
      if (focusRef) focusRef.current = null;
      touchListenerCleanup?.();
      observer.disconnect();
      ws?.close();
      term?.dispose();
    };
  }, [session.id]);

  return (
    <div className={[styles.panel, fullscreen ? styles.panelFullscreen : ''].filter(Boolean).join(' ')}>
      <div className={styles.titleBar}>
        <span className={styles.sessionName}>{session.name || session.id.slice(0, 8)}</span>
        {session.tmuxSession && (
          <span className={styles.tmuxTarget}>{session.tmuxSession}</span>
        )}
        {onToggleFullscreen && (
          <button
            type="button"
            className={styles.fullscreenBtn}
            onClick={onToggleFullscreen}
            title={fullscreen ? 'Exit fullscreen (Esc)' : 'Fullscreen (Ctrl+Shift+F)'}
            aria-label={fullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
          >
            {fullscreen ? '↙' : '⤢'}
          </button>
        )}
      </div>
      <div ref={containerRef} className={styles.terminal} data-gru-terminal />
    </div>
  );
}
