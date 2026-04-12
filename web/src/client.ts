import { createClient } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { GruService } from './gen/proto/gru/v1/gru_pb';

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
