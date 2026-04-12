import styles from './AttentionIndicator.module.css';

interface AttentionIndicatorProps {
  score: number; // 0.0–1.0
}

export function AttentionIndicator({ score }: AttentionIndicatorProps) {
  const pct = Math.round(score * 100);
  const fillClass =
    score >= 0.7
      ? styles.high
      : score >= 0.4
      ? styles.medium
      : styles.low;

  return (
    <div className={styles.wrapper} aria-label={`Attention score: ${pct}%`}>
      <div className={styles.track}>
        <div
          className={[styles.fill, fillClass].join(' ')}
          style={{ width: `${pct}%` }}
          role="progressbar"
          aria-valuenow={pct}
          aria-valuemin={0}
          aria-valuemax={100}
        />
      </div>
      <span className={styles.label}>{pct}%</span>
    </div>
  );
}
