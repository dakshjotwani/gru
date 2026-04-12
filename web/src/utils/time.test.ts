import { describe, it, expect } from 'vitest';
import { formatDuration, uptimeSeconds } from './time';

describe('formatDuration', () => {
  it('formats seconds under a minute', () => {
    expect(formatDuration(0)).toBe('0s');
    expect(formatDuration(45)).toBe('45s');
    expect(formatDuration(59)).toBe('59s');
  });

  it('formats minutes and seconds', () => {
    expect(formatDuration(60)).toBe('1m');
    expect(formatDuration(134)).toBe('2m 14s');
    expect(formatDuration(3599)).toBe('59m 59s');
  });

  it('formats hours and minutes', () => {
    expect(formatDuration(3600)).toBe('1h');
    expect(formatDuration(3660)).toBe('1h 1m');
    expect(formatDuration(7534)).toBe('2h 5m');
  });

  it('formats days and hours', () => {
    expect(formatDuration(86400)).toBe('1d');
    expect(formatDuration(90000)).toBe('1d 1h');
    expect(formatDuration(172800)).toBe('2d');
  });

  it('handles negative values', () => {
    expect(formatDuration(-5)).toBe('0s');
  });
});

describe('uptimeSeconds', () => {
  it('computes difference from a given now', () => {
    const startedAt = 1000; // seconds
    const nowMs = 1060 * 1000; // 1060 seconds in ms
    expect(uptimeSeconds(startedAt, nowMs)).toBe(60);
  });

  it('handles bigint startedAt', () => {
    const startedAt = BigInt(1000);
    const nowMs = 1060 * 1000;
    expect(uptimeSeconds(startedAt, nowMs)).toBe(60);
  });

  it('returns 0 for future started_at', () => {
    const startedAt = 9999999999;
    expect(uptimeSeconds(startedAt, 1000)).toBe(0);
  });
});
