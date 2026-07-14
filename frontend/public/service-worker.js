/* Windshift mobile PWA service worker.
 * Conservative: network-only application assets and API calls, a self-contained
 * recovery document for failed navigation, plus Web Push. We deliberately do
 * not cache index.html without its versioned asset graph: that produces an
 * unbootable white page when the device is offline. */
const CACHE_PREFIX = 'windshift-pwa-';
const CACHE_VERSION = 'v2';
const LEGACY_CACHE_NAMES = new Set(['windshift-shell-v1']);
const RECOVERY_KEY = 'recovery-document';
const NAVIGATION_TIMEOUT_MS = 10_000;
const scopePath = new URL(self.registration.scope).pathname;
const scopeKey = encodeURIComponent(scopePath.replace(/^\/+|\/+$/g, '') || 'root');
const CACHE_FAMILY = `${CACHE_PREFIX}${scopeKey}-`;
const CACHE = `${CACHE_FAMILY}${CACHE_VERSION}`;

function recoveryResponse() {
  return new Response(
    `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
    <meta name="theme-color" content="#ffffff" />
    <title>Windshift — Connection problem</title>
    <style>
      :root { color-scheme: light dark; font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
      * { box-sizing: border-box; }
      body { margin: 0; min-height: 100vh; min-height: 100dvh; display: grid; place-items: center; background: #fff; color: #172b4d; }
      main { width: min(100% - 2rem, 28rem); padding: 2rem; text-align: center; }
      .mark { width: 3.5rem; height: 3.5rem; margin: 0 auto 1.25rem; border: 4px solid #dfe1e6; border-top-color: #0c66e4; border-radius: 50%; animation: spin 1s linear infinite; }
      h1 { margin: 0 0 .75rem; font-size: 1.25rem; }
      p { margin: 0 0 1.5rem; line-height: 1.5; color: #44546f; }
      button { min-height: 2.75rem; padding: .65rem 1.25rem; border: 0; border-radius: .5rem; background: #0c66e4; color: #fff; font: inherit; font-weight: 600; cursor: pointer; }
      @keyframes spin { to { transform: rotate(360deg); } }
      @media (prefers-color-scheme: dark) {
        body { background: #1d2125; color: #f7f8f9; }
        p { color: #b6c2cf; }
        .mark { border-color: #454f59; border-top-color: #579dff; }
      }
      @media (prefers-reduced-motion: reduce) { .mark { animation: none; } }
    </style>
  </head>
  <body>
    <main>
      <div class="mark" aria-hidden="true"></div>
      <h1>Windshift couldn't connect</h1>
      <p>Check your connection or server, then try again.</p>
      <button id="retry" type="button">Retry</button>
    </main>
    <script>
      document.getElementById('retry').addEventListener('click', () => location.reload());
      window.addEventListener('online', () => location.reload(), { once: true });
    </script>
  </body>
</html>`,
    {
      status: 503,
      statusText: 'Service Unavailable',
      headers: {
        'Content-Type': 'text/html; charset=utf-8',
        'Cache-Control': 'no-store',
      },
    }
  );
}

async function cachedRecoveryResponse() {
  try {
    const cache = await caches.open(CACHE);
    return (await cache.match(RECOVERY_KEY)) || recoveryResponse();
  } catch {
    return recoveryResponse();
  }
}

async function fetchNavigation(req) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), NAVIGATION_TIMEOUT_MS);
  try {
    return await fetch(req, { signal: controller.signal });
  } finally {
    clearTimeout(timeout);
  }
}

function isRetryableServerFailure(response) {
  return response.status === 408 || response.status === 429 || response.status >= 500;
}

function scopedURL(value = 'm') {
  if (/^https?:\/\//i.test(value)) return value;
  return new URL(String(value).replace(/^\/+/, ''), self.registration.scope).href;
}

self.addEventListener('install', (event) => {
  // Cache only the dependency-free recovery document. A normal app document is
  // safe only together with its exact hashed asset graph.
  event.waitUntil(
    Promise.all([
      self.skipWaiting(),
      caches.open(CACHE).then((cache) => cache.put(RECOVERY_KEY, recoveryResponse())),
    ])
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      const keys = await caches.keys();
      await Promise.all(
        keys
          .filter(
            (key) => LEGACY_CACHE_NAMES.has(key) || (key.startsWith(CACHE_FAMILY) && key !== CACHE)
          )
          .map((key) => caches.delete(key))
      );
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
        const res = await fetchNavigation(req);
        if (isRetryableServerFailure(res)) return cachedRecoveryResponse();
        return res;
      } catch {
        return cachedRecoveryResponse();
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
    data: { url: scopedURL(payload.url || 'm') },
    icon: scopedURL('apple-touch-icon.png'),
    badge: scopedURL('favicon-32x32.png'),
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const url = scopedURL(event.notification.data?.url || 'm');
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
