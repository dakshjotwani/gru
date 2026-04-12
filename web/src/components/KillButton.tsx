import { useState } from 'react';
import { gruClient } from '../client';
import styles from './KillButton.module.css';

interface KillButtonProps {
  sessionId: string;
  onKilled?: () => void;
}

export function KillButton({ sessionId, onKilled }: KillButtonProps) {
  const [confirming, setConfirming] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const shortId = sessionId.slice(0, 8);

  function handleClick() {
    setConfirming(true);
    setError(null);
  }

  function handleCancel() {
    setConfirming(false);
  }

  async function handleConfirm() {
    setLoading(true);
    setError(null);
    try {
      await gruClient.killSession({ id: sessionId });
      setConfirming(false);
      onKilled?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to kill session');
    } finally {
      setLoading(false);
    }
  }

  if (confirming) {
    return (
      <div className={styles.confirm} role="dialog" aria-label={`Kill session ${shortId}?`}>
        <span className={styles.question}>Kill session {shortId}?</span>
        <button
          className={styles.confirmBtn}
          onClick={handleConfirm}
          disabled={loading}
          aria-label="Confirm kill"
        >
          {loading ? 'Killing…' : 'Confirm'}
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

  return (
    <button className={styles.killBtn} onClick={handleClick} aria-label={`Kill session ${shortId}`}>
      Kill
    </button>
  );
}
