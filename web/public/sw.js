// Service worker for Gru.
//
// Responsibilities:
//   - App-shell caching so the PWA loads to a "no connection" screen
//     when Tailscale is off instead of a blank page.
//   - Web Push: render incoming pushes as notifications with optional
//     Approve/Deny action buttons (for permission-prompt events).
//   - notificationclick: fire `POST /actions/<token>?a=approve|deny`
//     for action taps, or open the session deep-link for plain taps.
//   - pushsubscriptionchange: re-register the device with the new
//     endpoint via `PUT /devices/<id>`.
//   - Legacy: the page-driven NOTIFY_ATTENTION postMessage still works
//     so pre-PWA-shell desktop flows don't regress.

const SHELL_CACHE = 'gru-shell-v1';
const SHELL_URLS = ['/', '/index.html', '/favicon.svg', '/manifest.json'];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(SHELL_CACHE).then((cache) =>
      cache.addAll(SHELL_URLS).catch(() => {
        // Best-effort cache priming; missing assets shouldn't block install.
      })
    )
  );
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    Promise.all([
      caches.keys().then((keys) =>
        Promise.all(keys.filter((k) => k !== SHELL_CACHE).map((k) => caches.delete(k)))
      ),
      self.clients.claim(),
    ])
  );
});

// Web Push: the server signs payload with VAPID; the browser delivers
// a PushEvent here. Payload shape (matches internal/push):
//   { title, body, tag, actions: [{action, title}], data: { sessionId, eventId, actionToken } }
self.addEventListener('push', (event) => {
  if (!event.data) return;
  let payload;
  try {
    payload = event.data.json();
  } catch {
    payload = { title: 'Gru', body: event.data.text() };
  }
  const options = {
    body: payload.body || '',
    tag: payload.tag || `gru-${Date.now()}`,
    icon: '/favicon.svg',
    badge: '/favicon.svg',
    data: payload.data || {},
    requireInteraction: !!payload.requireInteraction,
  };
  if (Array.isArray(payload.actions)) options.actions = payload.actions;
  event.waitUntil(self.registration.showNotification(payload.title || 'Gru', options));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const data = event.notification.data || {};
  const action = event.action;

  // Lock-screen action button: approve/deny without opening the app.
  if ((action === 'approve' || action === 'deny') && data.actionToken) {
    event.waitUntil(
      fetch(`/actions/${encodeURIComponent(data.actionToken)}?a=${action}`, {
        method: 'POST',
      })
        .then((resp) => {
          // Best-effort confirmation toast. Auto-dismisses via requireInteraction=false.
          const label =
            resp.ok ? (action === 'approve' ? '✓ Approved' : '✗ Denied') :
            resp.status === 410 ? 'Too late — open Gru' :
            resp.status === 409 ? 'Already resolved' :
            'Couldn’t reach Gru';
          return self.registration.showNotification(label, {
            tag: `gru-ack-${data.eventId || Date.now()}`,
            icon: '/favicon.svg',
            requireInteraction: false,
          });
        })
        .catch(() =>
          self.registration.showNotification('Couldn’t reach Gru', {
            tag: `gru-ack-${data.eventId || Date.now()}`,
            icon: '/favicon.svg',
          })
        )
    );
    return;
  }

  // Plain tap: open (or focus) the dashboard, deep-linked to the session.
  const url = data.sessionId ? `/sessions/${data.sessionId}` : '/';
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((clients) => {
      for (const client of clients) {
        if ('focus' in client) {
          client.focus();
          if ('navigate' in client) client.navigate(url).catch(() => {});
          return;
        }
      }
      if (self.clients.openWindow) return self.clients.openWindow(url);
    })
  );
});

// pushsubscriptionchange: the browser rotated our subscription; let the
// server know so it keeps delivering to the new endpoint. The device ID
// lives in localStorage on the page; the SW doesn't have access to it,
// so we postMessage the clients and let the page re-register.
self.addEventListener('pushsubscriptionchange', (event) => {
  event.waitUntil(
    self.clients.matchAll().then((clients) => {
      for (const client of clients) {
        client.postMessage({ type: 'PUSH_SUBSCRIPTION_CHANGED' });
      }
    })
  );
});

// Legacy page→SW notification channel (desktop dashboard focus-out).
self.addEventListener('message', (event) => {
  if (!event.data) return;
  if (event.data.type === 'NOTIFY_ATTENTION') {
    const { sessionId, projectName } = event.data;
    const shortId = sessionId.slice(0, 8);
    self.registration.showNotification('Gru — Attention needed', {
      body: `Session ${shortId} in ${projectName} needs your attention`,
      tag: `gru-attention-${sessionId}`,
      icon: '/favicon.svg',
      requireInteraction: false,
    });
  }
});
