import { describe, it, expect } from 'vitest';
import { getStatusDisplay, isTerminalStatus } from './status';
import { SessionStatus } from '../types';

describe('getStatusDisplay', () => {
  it('returns red/orange class for needs_attention', () => {
    const d = getStatusDisplay(SessionStatus.NEEDS_ATTENTION);
    expect(d.label).toBe('Needs Attention');
    expect(d.colorClass).toBe('statusNeedsAttention');
    expect(d.pulsing).toBe(false);
  });

  it('returns green class for running', () => {
    const d = getStatusDisplay(SessionStatus.RUNNING);
    expect(d.colorClass).toBe('statusRunning');
    expect(d.pulsing).toBe(false);
  });

  it('returns yellow class for idle', () => {
    const d = getStatusDisplay(SessionStatus.IDLE);
    expect(d.colorClass).toBe('statusIdle');
  });

  it('returns blue + pulsing for starting', () => {
    const d = getStatusDisplay(SessionStatus.STARTING);
    expect(d.colorClass).toBe('statusStarting');
    expect(d.pulsing).toBe(true);
  });

  it('returns gray class for completed', () => {
    const d = getStatusDisplay(SessionStatus.COMPLETED);
    expect(d.colorClass).toBe('statusCompleted');
  });

  it('returns red class for errored', () => {
    const d = getStatusDisplay(SessionStatus.ERRORED);
    expect(d.colorClass).toBe('statusErrored');
  });

  it('returns gray class for killed', () => {
    const d = getStatusDisplay(SessionStatus.KILLED);
    expect(d.colorClass).toBe('statusKilled');
  });

  it('returns unknown for unspecified', () => {
    const d = getStatusDisplay(SessionStatus.UNSPECIFIED);
    expect(d.label).toBe('Unknown');
  });
});

describe('isTerminalStatus', () => {
  it('returns true for completed, errored, killed', () => {
    expect(isTerminalStatus(SessionStatus.COMPLETED)).toBe(true);
    expect(isTerminalStatus(SessionStatus.ERRORED)).toBe(true);
    expect(isTerminalStatus(SessionStatus.KILLED)).toBe(true);
  });

  it('returns false for running, idle, starting, needs_attention', () => {
    expect(isTerminalStatus(SessionStatus.RUNNING)).toBe(false);
    expect(isTerminalStatus(SessionStatus.IDLE)).toBe(false);
    expect(isTerminalStatus(SessionStatus.STARTING)).toBe(false);
    expect(isTerminalStatus(SessionStatus.NEEDS_ATTENTION)).toBe(false);
  });
});
