// Service worker for Gru desktop notifications.
// The page posts messages here when sessions need attention
// while the tab is not focused.

self.addEventListener('install', () => {
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

// Listen for messages from the page.
self.addEventListener('message', (event) => {
  if (!event.data) return;

  if (event.data.type === 'NOTIFY_ATTENTION') {
    const { sessionId, projectName } = event.data;
    const shortId = sessionId.slice(0, 8);

    self.registration.showNotification('Gru — Attention needed', {
      body: `Session ${shortId} in ${projectName} needs your attention`,
      tag: `gru-attention-${sessionId}`,
      icon: '/vite.svg',
      requireInteraction: false,
    });
  }
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: 'window' }).then((clientList) => {
      for (const client of clientList) {
        if ('focus' in client) return client.focus();
      }
      if (self.clients.openWindow) return self.clients.openWindow('/');
    })
  );
});
