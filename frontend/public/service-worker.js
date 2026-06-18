/* Windshift mobile PWA service worker.
 * Conservative: app-shell caching for offline navigation fallback + Web Push.
 * Hashed asset requests are passed straight through (never intercepted), so a
 * stale SW can't serve mismatched chunks. */
const CACHE = 'windshift-shell-v1';

self.addEventListener('install', () => {
  // Activate immediately so push works on first install.
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      const keys = await caches.keys();
      await Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)));
      await self.clients.claim();
    })()
  );
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  // Only intercept top-level navigations; everything else (hashed assets, API)
  // goes to the network untouched.
  if (req.mode !== 'navigate') return;

  event.respondWith(
    (async () => {
      try {
        const res = await fetch(req);
        const cache = await caches.open(CACHE);
        cache.put('app-shell', res.clone());
        return res;
      } catch {
        const cached = await caches.match('app-shell');
        return cached || Response.error();
      }
    })()
  );
});

// --- Web Push ---
self.addEventListener('push', (event) => {
  let payload = {};
  try {
    payload = event.data ? event.data.json() : {};
  } catch {
    payload = { title: 'Windshift', body: event.data ? event.data.text() : '' };
  }

  const title = payload.title || 'Windshift';
  const options = {
    body: payload.body || '',
    tag: payload.tag || payload.id || undefined,
    data: { url: payload.url || '/m' },
    icon: 'apple-touch-icon.png',
    badge: 'favicon-32x32.png',
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const url = event.notification.data?.url || '/m';
  event.waitUntil(
    (async () => {
      const clientList = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
      for (const client of clientList) {
        if ('focus' in client) {
          await client.focus();
          if ('navigate' in client) {
            try {
              await client.navigate(url);
            } catch {
              /* cross-origin / not allowed */
            }
          }
          return;
        }
      }
      if (self.clients.openWindow) await self.clients.openWindow(url);
    })()
  );
});
