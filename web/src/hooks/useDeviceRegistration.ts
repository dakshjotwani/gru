// Web Push + device registration for the installed PWA.
//
// Flow:
//   1. On mount, check if the PWA is running standalone (installed to
//      home screen on iOS, or installed as a PWA on desktop).
//   2. If not registered yet and the user hasn't declined, expose a
//      `requestSubscription()` action that the UI can bind to a button.
//   3. On grant, subscribe to Web Push with the server's VAPID public
//      key (fetched from /push/public-key), and POST the subscription
//      to /devices to get a device ID back. Persist the ID to
//      localStorage for future endpoint rotations.
//   4. Listen for PUSH_SUBSCRIPTION_CHANGED from the service worker
//      (fires when the browser rotates the endpoint) and PUT the new
//      subscription to /devices/:id.
//
// Server URL: the PWA is served from the same origin as the gru
// server (Vite dev proxies /devices to the backend in dev; in prod
// the backend serves the static shell).

import { useCallback, useEffect, useState } from 'react';

const DEVICE_ID_KEY = 'gru.deviceId';

const serverUrl = import.meta.env.VITE_GRU_SERVER_URL ?? '';

export type PushPermission = 'default' | 'granted' | 'denied' | 'unsupported';

export interface DeviceRegistration {
  permission: PushPermission;
  registered: boolean;
  deviceId: string | null;
  requestSubscription: (label?: string) => Promise<void>;
  error: string | null;
}

export function useDeviceRegistration(): DeviceRegistration {
  const [permission, setPermission] = useState<PushPermission>('default');
  const [deviceId, setDeviceId] = useState<string | null>(
    () => localStorage.getItem(DEVICE_ID_KEY)
  );
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!('Notification' in window) || !('serviceWorker' in navigator) || !('PushManager' in window)) {
      setPermission('unsupported');
      return;
    }
    setPermission(Notification.permission as PushPermission);
  }, []);

  // Listen for the SW telling us the subscription rotated.
  useEffect(() => {
    if (!('serviceWorker' in navigator)) return;
    const handler = async (event: MessageEvent) => {
      if (event.data?.type !== 'PUSH_SUBSCRIPTION_CHANGED') return;
      const id = localStorage.getItem(DEVICE_ID_KEY);
      if (!id) return;
      try {
        const sub = await getFreshSubscription();
        if (!sub) return;
        await fetch(`${serverUrl}/devices/${encodeURIComponent(id)}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(subscriptionToBody(sub)),
        });
      } catch (err) {
        console.warn('device rotation failed:', err);
      }
    };
    navigator.serviceWorker.addEventListener('message', handler);
    return () => navigator.serviceWorker.removeEventListener('message', handler);
  }, []);

  const requestSubscription = useCallback(async (label?: string) => {
    setError(null);
    if (permission === 'unsupported') {
      setError('Web Push not supported in this browser.');
      return;
    }

    try {
      const perm = await Notification.requestPermission();
      setPermission(perm as PushPermission);
      if (perm !== 'granted') return;

      // Server's VAPID public key is fetched once per registration.
      const keyResp = await fetch(`${serverUrl}/push/public-key`);
      if (!keyResp.ok) {
        setError('server has no VAPID key configured');
        return;
      }
      const { publicKey } = await keyResp.json();

      const reg = await navigator.serviceWorker.ready;
      const sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(publicKey),
      });

      const resp = await fetch(`${serverUrl}/devices`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          label: label || deriveDeviceLabel(),
          ...subscriptionToBody(sub),
        }),
      });
      if (!resp.ok) {
        setError(`register failed: ${resp.status}`);
        return;
      }
      const { id } = (await resp.json()) as { id: string };
      localStorage.setItem(DEVICE_ID_KEY, id);
      setDeviceId(id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [permission]);

  return {
    permission,
    registered: !!deviceId,
    deviceId,
    requestSubscription,
    error,
  };
}

async function getFreshSubscription(): Promise<PushSubscription | null> {
  const reg = await navigator.serviceWorker.ready;
  return reg.pushManager.getSubscription();
}

function subscriptionToBody(sub: PushSubscription) {
  const json = sub.toJSON();
  return {
    endpoint: json.endpoint!,
    p256dh: json.keys?.p256dh ?? '',
    auth: json.keys?.auth ?? '',
  };
}

function deriveDeviceLabel(): string {
  const ua = navigator.userAgent;
  if (/iPhone/i.test(ua)) return 'iPhone';
  if (/iPad/i.test(ua)) return 'iPad';
  if (/Android/i.test(ua)) return 'Android';
  if (/Mac/i.test(ua)) return 'Mac';
  if (/Windows/i.test(ua)) return 'Windows';
  return 'Device';
}

function urlBase64ToUint8Array(base64: string): Uint8Array<ArrayBuffer> {
  const padding = '='.repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(b64);
  // Allocate a plain ArrayBuffer explicitly so this typed-array is
  // compatible with BufferSource (which excludes SharedArrayBuffer).
  const buf = new ArrayBuffer(raw.length);
  const out = new Uint8Array(buf);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}
