/* Mobile PWA service-worker registration + Web Push subscription lifecycle.
 * Registration resolves paths against document.baseURI so it works under
 * context-path deployments and the injected <base href>. */
import { fetchAPI } from '../api/core.js';

let registrationPromise = null;

/**
 * Register the mobile service worker. Idempotent — repeated calls return the
 * same in-flight/settled registration. No-ops where service workers are
 * unavailable (older browsers, insecure contexts).
 * @returns {Promise<ServiceWorkerRegistration|null>}
 */
export function registerMobileServiceWorker() {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) {
    return Promise.resolve(null);
  }
  if (registrationPromise) return registrationPromise;

  const swUrl = new URL('service-worker.js', document.baseURI).pathname;
  const scope = new URL('./', document.baseURI).pathname;

  registrationPromise = navigator.serviceWorker.register(swUrl, { scope }).catch((err) => {
    console.warn('[mobile] service worker registration failed:', err);
    return null;
  });

  return registrationPromise;
}

/** True when launched as an installed PWA (iOS requires this for Web Push). */
export function isStandalone() {
  if (typeof window === 'undefined') return false;
  return (
    window.matchMedia?.('(display-mode: standalone)').matches ||
    // iOS Safari exposes the non-standard navigator.standalone for Home-Screen
    // apps (not in the TS DOM lib, hence the cast).
    /** @type {any} */ (window.navigator).standalone === true
  );
}

/** Whether the browser can do Web Push at all. */
export function pushSupported() {
  return (
    typeof window !== 'undefined' &&
    'serviceWorker' in navigator &&
    'PushManager' in window &&
    'Notification' in window
  );
}

/** Current push state for the enable-notifications UI. */
export async function getPushState() {
  const supported = pushSupported();
  const installed = isStandalone();
  let permission = supported ? Notification.permission : 'denied';
  let subscribed = false;
  if (supported) {
    try {
      const reg = await registerMobileServiceWorker();
      const sub = reg && (await reg.pushManager.getSubscription());
      subscribed = !!sub;
    } catch {
      subscribed = false;
    }
  }
  return { supported, installed, permission, subscribed };
}

// Web Push VAPID keys arrive base64url-encoded; the PushManager needs raw bytes.
function urlBase64ToUint8Array(base64String) {
  const padding = '='.repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(base64);
  const output = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) output[i] = raw.charCodeAt(i);
  return output;
}

/**
 * Enable push: request permission (must be from a user gesture), subscribe via
 * the PushManager using the server's VAPID key, and persist the subscription.
 * @returns {Promise<{ok: boolean, reason?: string}>}
 */
export async function enablePush() {
  if (!pushSupported()) return { ok: false, reason: 'unsupported' };

  // Request permission FIRST, synchronously inside the click's user-gesture
  // (transient activation) window. iOS Safari silently no-ops the permission
  // prompt once it has awaited anything (a network fetch below), which made
  // the "Enable notifications" button appear dead on iOS PWAs. Chrome/Android
  // are more lenient, so this never surfaced there.
  const permission = await Notification.requestPermission();
  if (permission !== 'granted') return { ok: false, reason: 'denied' };

  let config;
  try {
    config = await fetchAPI('/push/vapid-public-key');
  } catch {
    return { ok: false, reason: 'config' };
  }
  if (!config?.enabled || !config?.public_key) return { ok: false, reason: 'disabled' };

  const reg = await registerMobileServiceWorker();
  if (!reg) return { ok: false, reason: 'no-sw' };
  await navigator.serviceWorker.ready;

  let sub;
  try {
    sub = await reg.pushManager.getSubscription();
    if (!sub) {
      sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(config.public_key),
      });
    }
  } catch (err) {
    console.warn('[mobile] push subscribe failed:', err);
    return { ok: false, reason: 'subscribe' };
  }

  try {
    await fetchAPI('/push/subscriptions', { method: 'POST', body: JSON.stringify(sub.toJSON()) });
  } catch (err) {
    console.warn('[mobile] saving push subscription failed:', err);
    return { ok: false, reason: 'persist' };
  }
  return { ok: true };
}

/** Disable push: unsubscribe locally and remove the server-side record. */
export async function disablePush() {
  if (!pushSupported()) return { ok: true };
  const reg = await registerMobileServiceWorker();
  const sub = reg && (await reg.pushManager.getSubscription());
  if (!sub) return { ok: true };

  const endpoint = sub.endpoint;
  try {
    await sub.unsubscribe();
  } catch {
    /* ignore — the server will prune on the next 410 */
  }
  try {
    const subs = await fetchAPI('/push/subscriptions');
    const match = Array.isArray(subs) ? subs.find((s) => s.endpoint === endpoint) : null;
    if (match) await fetchAPI(`/push/subscriptions/${match.id}`, { method: 'DELETE' });
  } catch {
    /* best-effort cleanup */
  }
  return { ok: true };
}

/** Send a test push to the current user's devices. */
export function sendTestPush() {
  return fetchAPI('/push/test', { method: 'POST' });
}

/**
 * Run a push diagnostic: send a test push and interpret the server's per-device
 * delivery result against this device's local subscription. Returns a verdict
 * the UI can show verbatim so a user (or a human helping them) can tell which of
 * the three WI-472 failure modes is live without server access:
 *   - 'unsupported'  push isn't available in this browser
 *   - 'no-server-sub' the server has no subscription at all → re-enable
 *   - 'device-missing' the server has other devices but not this one → re-enable
 *   - 'rejected'     the push provider rejected delivery (detail has the reason)
 *   - 'delivered'    the provider accepted it; if no banner showed, iOS suppressed it
 *   - 'error'        the request itself failed
 * @returns {Promise<{verdict: string, detail: string}>}
 */
export async function runPushDiagnostic() {
  if (!pushSupported())
    return { verdict: 'unsupported', detail: 'This browser cannot receive push notifications.' };

  let localEndpoint = null;
  try {
    const reg = await registerMobileServiceWorker();
    const sub = reg && (await reg.pushManager.getSubscription());
    localEndpoint = sub?.endpoint || null;
  } catch {
    /* fall through — the server result still tells us most of the story */
  }

  let res;
  try {
    res = await fetchAPI('/push/test', { method: 'POST' });
  } catch (err) {
    return { verdict: 'error', detail: `Test request failed: ${err?.message || err}` };
  }

  const results = Array.isArray(res?.results) ? res.results : [];
  if (results.length === 0) {
    return {
      verdict: 'no-server-sub',
      detail:
        'The server has no push subscription for your account. Turn notifications off and on again to re-register this device.',
    };
  }

  const mine = localEndpoint ? results.find((r) => r.endpoint === localEndpoint) : null;
  if (!mine) {
    return {
      verdict: 'device-missing',
      detail: `The server has ${results.length} subscription(s), but none match this device. Turn notifications off and on again here.`,
    };
  }
  if (mine.ok) {
    return {
      verdict: 'delivered',
      detail: `Delivered to the push provider (HTTP ${mine.status_code}). If no banner appeared, iOS suppressed it — check that the app is backgrounded and that no duplicate is still on screen.`,
    };
  }
  return {
    verdict: 'rejected',
    detail: `The push provider rejected delivery (HTTP ${mine.status_code || '—'}): ${mine.error || 'unknown error'}`,
  };
}
