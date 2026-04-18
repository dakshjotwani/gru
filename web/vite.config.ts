import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// GRU_WEB_PORT lets the gru-on-gru minion flow start on an ephemeral port
// (value 0 = OS picks) without touching this file. Default matches the
// prior hardcoded value so the human `make dev` path is unchanged.
const port = Number(process.env.GRU_WEB_PORT ?? 3001);

export default defineConfig({
  plugins: [react()],
  server: {
    port,
    host: '0.0.0.0',
    allowedHosts: true,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
  },
});
