/* Mobile PWA service-worker registration + Web Push subscription lifecycle.
 * Registration resolves paths against document.baseURI so it works under
 * context-path deployments and the injected <base href>. */
import { fetchAPI } from '../api/core.js';
import { registerMobileServiceWorker } from './serviceWorkerClient.js';

export { registerMobileServiceWorker } from './serviceWorkerClient.js';

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
