import type { Session, SessionEvent } from '../types';
import { SessionStatus } from '../types';

/** Active attention signals for one session, derived on the client.
 *
 * The backend attention engine tracks these same signals internally and rolls
 * them up into `attention_score`, but doesn't (yet) persist the individual
 * signal breakdown. Until it does, we reconstruct what we can from `status`,
 * the recent event stream, and staleness of `last_event_at`. These labels
 * line up with the engine's signal names (spec §Attention score) so when
 * server-side persistence lands, the UI can swap to authoritative values.
 */
export interface Signals {
  paused: boolean;
  notification: boolean;
  toolError: boolean;
  stale: boolean;
}

/** Seconds after which a running session with no new events is "stale." Matches
 *  the attention engine's default StalenessStart (5 min). */
export const STALE_AFTER_SECONDS = 5 * 60;

function tsToEpoch(ts: unknown): number | null {
  if (!ts) return null;
  if (typeof ts === 'object' && ts !== null && 'seconds' in ts) {
    const secs = Number((ts as { seconds: unknown }).seconds);
    if (!isNaN(secs) && secs > 0) return secs;
  }
  if (typeof ts === 'string') {
    const ms = Date.parse(ts);
    if (!isNaN(ms)) return ms / 1000;
  }
  return null;
}

export function computeSignals(session: Session, events: SessionEvent[] = []): Signals {
  const paused = session.status === SessionStatus.IDLE;
  const notification = session.status === SessionStatus.NEEDS_ATTENTION;

  let toolError = false;
  for (let i = events.length - 1; i >= 0; i--) {
    const t = events[i].type;
    // tool.pre / session.start / subagent.start reset the signal server-side,
    // so mirror that here — only the most recent "work-clearing" event matters.
    if (t === 'tool.pre' || t === 'session.start' || t === 'subagent.start') break;
    if (t === 'tool.error') {
      toolError = true;
      break;
    }
  }

  let stale = false;
  if (session.status === SessionStatus.RUNNING) {
    const last = tsToEpoch(session.lastEventAt);
    if (last !== null) {
      const ageSeconds = Date.now() / 1000 - last;
      if (ageSeconds >= STALE_AFTER_SECONDS) stale = true;
    }
  }

  return { paused, notification, toolError, stale };
}

export function activeSignalCount(s: Signals): number {
  let n = 0;
  if (s.paused) n++;
  if (s.notification) n++;
  if (s.toolError) n++;
  if (s.stale) n++;
  return n;
}
