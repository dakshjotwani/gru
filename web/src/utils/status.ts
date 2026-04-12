import { SessionStatus } from '../types';

export interface StatusDisplay {
  label: string;
  colorClass: string;
  pulsing: boolean;
}

export function getStatusDisplay(status: SessionStatus): StatusDisplay {
  switch (status) {
    case SessionStatus.NEEDS_ATTENTION:
      return { label: 'Needs Attention', colorClass: 'statusNeedsAttention', pulsing: false };
    case SessionStatus.RUNNING:
      return { label: 'Running', colorClass: 'statusRunning', pulsing: false };
    case SessionStatus.IDLE:
      return { label: 'Idle', colorClass: 'statusIdle', pulsing: false };
    case SessionStatus.STARTING:
      return { label: 'Starting', colorClass: 'statusStarting', pulsing: true };
    case SessionStatus.COMPLETED:
      return { label: 'Completed', colorClass: 'statusCompleted', pulsing: false };
    case SessionStatus.ERRORED:
      return { label: 'Errored', colorClass: 'statusErrored', pulsing: false };
    case SessionStatus.KILLED:
      return { label: 'Killed', colorClass: 'statusKilled', pulsing: false };
    case SessionStatus.UNSPECIFIED:
    default:
      return { label: 'Unknown', colorClass: 'statusUnknown', pulsing: false };
  }
}

export function isTerminalStatus(status: SessionStatus): boolean {
  return (
    status === SessionStatus.COMPLETED ||
    status === SessionStatus.ERRORED ||
    status === SessionStatus.KILLED
  );
}
