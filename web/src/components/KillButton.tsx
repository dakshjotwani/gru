import { useState } from 'react';
import { gruClient } from '../client';
import styles from './KillButton.module.css';

interface KillButtonProps {
  sessionId: string;
  onKilled?: () => void;
  /** compact renders a small × button intended for sidebar cards where space
   *  is tight. Confirm UI overlays in place (inline kebab-style) so it doesn't
   *  expand the card height. */
  compact?: boolean;
  /** "kill" (default) terminates a live session via KillSession. "delete"
   *  removes a terminal session's row from the DB via DeleteSession; the × is
   *  the same glyph but the action and confirm copy swap. */
  mode?: 'kill' | 'delete';
}

export function KillButton({ sessionId, onKilled, compact, mode = 'kill' }: KillButtonProps) {
  const [confirming, setConfirming] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const shortId = sessionId.slice(0, 8);
  const verb = mode === 'delete' ? 'Remove' : 'Kill';

  function handleClick(e: React.MouseEvent) {
    e.stopPropagation();
    setConfirming(true);
    setError(null);
  }

  function handleCancel(e: React.MouseEvent) {
    e.stopPropagation();
    setConfirming(false);
  }

  async function handleConfirm(e: React.MouseEvent) {
    e.stopPropagation();
    setLoading(true);
    setError(null);
    try {
      if (mode === 'delete') {
        await gruClient.deleteSession({ id: sessionId });
      } else {
        await gruClient.killSession({ id: sessionId });
      }
      setConfirming(false);
      onKilled?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : `Failed to ${verb.toLowerCase()} session`);
    } finally {
      setLoading(false);
    }
  }

  if (confirming) {
    // Compact variant keeps the inline confirm tight (no shortId label) so the
    // sidebar card doesn't grow vertically when a user arms the kill/delete.
    if (compact) {
      return (
        <span className={styles.compactConfirm} role="dialog" aria-label={`${verb} minion ${shortId}?`}>
          <button
            className={styles.compactConfirmBtn}
            onClick={handleConfirm}
            disabled={loading}
            aria-label={`Confirm ${verb.toLowerCase()}`}
            title={`Confirm ${verb.toLowerCase()}`}
          >
            {loading ? '…' : '✓'}
          </button>
          <button
            className={styles.compactCancelBtn}
            onClick={handleCancel}
            disabled={loading}
            aria-label="Cancel"
            title="Cancel"
          >
            ✕
          </button>
          {error && <span className={styles.error} title={error}>!</span>}
        </span>
      );
    }
    return (
      <div className={styles.confirm} role="dialog" aria-label={`${verb} session ${shortId}?`}>
        <span className={styles.question}>{verb} session {shortId}?</span>
        <button
          className={styles.confirmBtn}
          onClick={handleConfirm}
          disabled={loading}
          aria-label={`Confirm ${verb.toLowerCase()}`}
        >
          {loading ? `${verb === 'Kill' ? 'Killing' : 'Removing'}…` : 'Confirm'}
        </button>
        <button
          className={styles.cancelBtn}
          onClick={handleCancel}
          disabled={loading}
          aria-label="Cancel"
        >
          Cancel
        </button>
        {error && <span className={styles.error}>{error}</span>}
      </div>
    );
  }

  if (compact) {
    return (
      <button
        className={styles.compactKillBtn}
        onClick={handleClick}
        aria-label={`${verb} minion ${shortId}`}
        title={mode === 'delete' ? 'Remove from list' : 'Kill minion'}
      >
        ×
      </button>
    );
  }
  return (
    <button className={styles.killBtn} onClick={handleClick} aria-label={`${verb} session ${shortId}`}>
      {verb}
    </button>
  );
}
