/**
 * Format a duration in seconds into a human-readable string.
 * Examples: "45s", "2m 14s", "1h 5m", "3d 2h"
 */
export function formatDuration(seconds: number): string {
  if (seconds < 0) return '0s';
  if (seconds < 60) return `${Math.floor(seconds)}s`;

  const minutes = Math.floor(seconds / 60);
  const secs = Math.floor(seconds % 60);

  if (minutes < 60) {
    return secs > 0 ? `${minutes}m ${secs}s` : `${minutes}m`;
  }

  const hours = Math.floor(minutes / 60);
  const mins = minutes % 60;

  if (hours < 24) {
    return mins > 0 ? `${hours}h ${mins}m` : `${hours}h`;
  }

  const days = Math.floor(hours / 24);
  const hrs = hours % 24;
  return hrs > 0 ? `${days}d ${hrs}h` : `${days}d`;
}

/**
 * Calculate uptime in seconds from a started_at timestamp (seconds since epoch).
 */
export function uptimeSeconds(startedAtSecs: bigint | number, nowMs?: number): number {
  const nowSecs = (nowMs ?? Date.now()) / 1000;
  return Math.max(0, nowSecs - Number(startedAtSecs));
}
