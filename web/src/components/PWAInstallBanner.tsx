// Shows the iOS "Add to Home Screen" hint when Gru is loaded in Safari
// (not installed yet), and an "Enable notifications" nudge once
// installed but not yet registered for Web Push. Dismissible.
//
// iOS Safari does NOT fire beforeinstallprompt, so a manual banner is
// the only way to guide operators through the Share → Add to Home
// Screen flow. Once added to the home screen, navigator.standalone
// becomes true and this banner rewrites itself into the notifications
// prompt.

import { useEffect, useState } from 'react';
import { useDeviceRegistration } from '../hooks/useDeviceRegistration';
import styles from './PWAInstallBanner.module.css';

const DISMISSED_KEY = 'gru.installBannerDismissed';

export function PWAInstallBanner() {
  const { permission, registered, requestSubscription, error } = useDeviceRegistration();
  const [dismissed, setDismissed] = useState<boolean>(
    () => localStorage.getItem(DISMISSED_KEY) === '1'
  );
  const [standalone, setStandalone] = useState<boolean>(false);
  const [isIOS, setIsIOS] = useState<boolean>(false);

  useEffect(() => {
    const st =
      (window.matchMedia && window.matchMedia('(display-mode: standalone)').matches) ||
      // iOS Safari exposes navigator.standalone, not typed in lib.dom.
      (navigator as unknown as { standalone?: boolean }).standalone === true;
    setStandalone(st);
    setIsIOS(/iPhone|iPad|iPod/i.test(navigator.userAgent));
  }, []);

  if (dismissed) return null;

  // Case 1: on iOS Safari, not installed → "Add to Home Screen" hint.
  if (isIOS && !standalone) {
    return (
      <div className={styles.banner}>
        <span className={styles.msg}>
          Install Gru: tap <b>Share</b> → <b>Add to Home Screen</b>.
        </span>
        <button className={styles.dismiss} onClick={() => dismiss(setDismissed)}>
          Not now
        </button>
      </div>
    );
  }

  // Case 2: installed (or not-iOS PWA-capable), notifications not yet
  // enabled → offer to enable.
  if (standalone && permission !== 'granted' && permission !== 'unsupported' && !registered) {
    return (
      <div className={styles.banner}>
        <span className={styles.msg}>
          Enable push notifications to see sessions that need attention when Gru isn't open.
          {error && <span className={styles.err}> ({error})</span>}
        </span>
        <button className={styles.primary} onClick={() => requestSubscription()}>
          Enable
        </button>
        <button className={styles.dismiss} onClick={() => dismiss(setDismissed)}>
          Not now
        </button>
      </div>
    );
  }

  return null;
}

function dismiss(setDismissed: (b: boolean) => void) {
  localStorage.setItem(DISMISSED_KEY, '1');
  setDismissed(true);
}
