import { useCallback, useEffect, useReducer } from 'react';
import { gruClient } from '../client';
import type { Artifact, SessionEvent, SessionLink } from '../types';

interface State {
  artifacts: Artifact[];
  links: SessionLink[];
  error: string | null;
}

type Action =
  | { type: 'RESET' }
  | { type: 'SET_ARTIFACTS'; artifacts: Artifact[] }
  | { type: 'SET_LINKS'; links: SessionLink[] }
  | { type: 'ADD_ARTIFACT'; artifact: Artifact }
  | { type: 'REMOVE_ARTIFACT'; id: string }
  | { type: 'ADD_LINK'; link: SessionLink }
  | { type: 'ERROR'; message: string };

function reducer(state: State, action: Action): State {
  switch (action.type) {
    case 'RESET':
      return { artifacts: [], links: [], error: null };
    case 'SET_ARTIFACTS':
      return { ...state, artifacts: action.artifacts, error: null };
    case 'SET_LINKS':
      return { ...state, links: action.links };
    case 'ADD_ARTIFACT': {
      // Idempotent: dedupe in case the snapshot raced with the live event.
      if (state.artifacts.some((a) => a.id === action.artifact.id)) return state;
      return { ...state, artifacts: [...state.artifacts, action.artifact] };
    }
    case 'REMOVE_ARTIFACT':
      return { ...state, artifacts: state.artifacts.filter((a) => a.id !== action.id) };
    case 'ADD_LINK': {
      if (state.links.some((l) => l.id === action.link.id)) return state;
      return { ...state, links: [...state.links, action.link] };
    }
    case 'ERROR':
      return { ...state, error: action.message };
    default:
      return state;
  }
}

// useSessionArtifacts loads the current session's artifacts + links via
// gRPC and subscribes to artifact.created / session_link.created events
// from the existing SubscribeEvents stream. We don't open a new stream —
// useSessionStream already has one open and routes events through window
// dispatch via a CustomEvent (see useSessionArtifacts -> fetchInitial).
//
// Implementation note: rather than plumb event delivery through a
// React-context provider, we attach a window-level listener for two
// custom events ('gru:artifact-event' and 'gru:link-event') that the
// useSessionStream reducer dispatches whenever it sees one. This keeps
// the change to useSessionStream minimal while letting any tab-bar
// component subscribe.
export function useSessionArtifacts(sessionId: string) {
  const [state, dispatch] = useReducer(reducer, {
    artifacts: [],
    links: [],
    error: null,
  });

  // Initial load over gRPC.
  useEffect(() => {
    let cancelled = false;
    dispatch({ type: 'RESET' });
    if (!sessionId) return;

    Promise.all([
      gruClient.listArtifacts({ sessionId }),
      gruClient.listSessionLinks({ sessionId }),
    ])
      .then(([artResp, linkResp]) => {
        if (cancelled) return;
        dispatch({ type: 'SET_ARTIFACTS', artifacts: artResp.artifacts });
        dispatch({ type: 'SET_LINKS', links: linkResp.links });
      })
      .catch((err) => {
        if (cancelled) return;
        const msg = err instanceof Error ? err.message : String(err);
        dispatch({ type: 'ERROR', message: msg });
      });

    return () => { cancelled = true; };
  }, [sessionId]);

  // Live updates: listen for the custom events fired by useSessionStream.
  useEffect(() => {
    if (!sessionId) return;
    const onArtifact = (e: Event) => {
      const ce = e as CustomEvent<{ event: SessionEvent }>;
      const evt = ce.detail.event;
      if (evt.sessionId !== sessionId) return;
      const payload = parseProtoPayload<Artifact>(evt.payload);
      if (payload && evt.type === 'artifact.created') {
        dispatch({ type: 'ADD_ARTIFACT', artifact: payload });
      }
    };
    const onLink = (e: Event) => {
      const ce = e as CustomEvent<{ event: SessionEvent }>;
      const evt = ce.detail.event;
      if (evt.sessionId !== sessionId) return;
      const payload = parseProtoPayload<SessionLink>(evt.payload);
      if (payload && evt.type === 'session_link.created') {
        dispatch({ type: 'ADD_LINK', link: payload });
      }
    };
    window.addEventListener('gru:artifact-event', onArtifact);
    window.addEventListener('gru:link-event', onLink);
    return () => {
      window.removeEventListener('gru:artifact-event', onArtifact);
      window.removeEventListener('gru:link-event', onLink);
    };
  }, [sessionId]);

  const removeArtifact = useCallback(async (id: string) => {
    try {
      await gruClient.deleteArtifact({ id });
      dispatch({ type: 'REMOVE_ARTIFACT', id });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      dispatch({ type: 'ERROR', message: msg });
    }
  }, []);

  return {
    artifacts: state.artifacts,
    links: state.links,
    error: state.error,
    removeArtifact,
  };
}

// parseProtoPayload unmarshals the JSON payload the server publishes for
// artifact.created / session_link.created events. The server uses
// protojson.MarshalOptions{UseEnumNumbers:true}.Marshal — which produces
// camelCase keys for proto3 message fields — so we can JSON.parse and the
// shape matches the TS type exactly.
function parseProtoPayload<T>(payload: unknown): T | null {
  try {
    const text = typeof payload === 'string'
      ? payload
      : new TextDecoder().decode(payload as Uint8Array);
    return JSON.parse(text) as T;
  } catch {
    return null;
  }
}
