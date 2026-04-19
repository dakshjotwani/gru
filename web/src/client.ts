import { createClient } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { GruService } from './gen/proto/gru/v1/gru_pb';
import { resolveServerUrl } from './utils/serverUrl';

const transport = createConnectTransport({
  baseUrl: resolveServerUrl(),
});

export const gruClient = createClient(GruService, transport);
