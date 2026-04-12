import { SessionStatus } from '../types';
import { getStatusDisplay } from '../utils/status';
import styles from './StatusBadge.module.css';

interface StatusBadgeProps {
  status: SessionStatus;
}

export function StatusBadge({ status }: StatusBadgeProps) {
  const { label, colorClass, pulsing } = getStatusDisplay(status);
  return (
    <span
      className={[
        styles.badge,
        styles[colorClass],
        pulsing ? styles.pulsing : '',
      ]
        .filter(Boolean)
        .join(' ')}
    >
      {label}
    </span>
  );
}
