// Resolve the Gru backend URL once, consistently, for both gRPC
// (connect-web) and the terminal WebSocket. The app runs in three
// modes:
//
//   1. Single-port prod. The Go backend serves web/dist under "/",
//      so the page origin IS the backend. Relative / same-origin
//      calls Just Work and survive whatever hostname the operator
//      exposes (tailnet, HTTPS proxy, localhost).
//
//   2. Vite dev, local. The frontend runs on :5173 but hits the
//      backend on :7777 (or whatever server port is configured).
//      The operator (or scripts/dev.sh) sets VITE_GRU_SERVER_URL
//      at build / dev-server start time.
//
//   3. Static site served from a CDN, backend elsewhere. Again
//      VITE_GRU_SERVER_URL is the explicit override.
//
// In dev mode the build-time env var is ALWAYS in
// import.meta.env.VITE_GRU_SERVER_URL; in prod the Go backend also
// injects a runtime hint (window.__GRU_SERVER_URL__) that wins if
// present, so the same bundle can be redeployed behind different
// hostnames without rebuilding. Neither override → same-origin.

declare global {
  interface Window {
    __GRU_SERVER_URL__?: string;
  }
}

export function resolveServerUrl(): string {
  const runtime = typeof window !== 'undefined' ? window.__GRU_SERVER_URL__ : undefined;
  if (runtime && runtime.length > 0) return runtime;

  const buildTime = import.meta.env.VITE_GRU_SERVER_URL;
  if (buildTime && buildTime.length > 0) return buildTime;

  // Same-origin: works for single-port prod, HTTPS proxy, tailnet, etc.
  if (typeof window !== 'undefined') return window.location.origin;

  // SSR / test fallback — callers that run during build-time
  // rendering need SOME value.
  return 'http://localhost:7777';
}

// WebSocket variant: same resolution but with ws:// / wss:// instead
// of http:// / https://. The terminal handler is mounted at the
// same origin as everything else.
export function resolveWebSocketUrl(): string {
  return resolveServerUrl().replace(/^http/, 'ws');
}
