import { createClient } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { GruService } from './gen/proto/gru/v1/gru_pb';

const serverUrl = import.meta.env.VITE_GRU_SERVER_URL ?? 'http://localhost:7777';

const transport = createConnectTransport({
  baseUrl: serverUrl,
});

export const gruClient = createClient(GruService, transport);
