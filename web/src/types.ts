// Re-export proto-generated types for convenient imports throughout the app.
export type {
  Session,
  SessionEvent,
  Project,
  AgentProfile,
  ListSessionsRequest,
  ListSessionsResponse,
  GetSessionRequest,
  LaunchSessionRequest,
  LaunchSessionResponse,
  KillSessionRequest,
  KillSessionResponse,
  ListProjectsRequest,
  ListProjectsResponse,
  ListProfilesRequest,
  ListProfilesResponse,
  SubscribeEventsRequest,
} from './gen/proto/gru/v1/gru_pb';

export { SessionStatus, GruService } from './gen/proto/gru/v1/gru_pb';
