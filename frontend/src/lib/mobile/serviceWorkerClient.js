import { addToast } from '../stores/toasts.svelte.js';

let registrationPromise = null;
let updateReloadRequested = false;
const UPDATE_APPLIED_KEY = 'pwa-just-updated';
const UPDATE_SUPPRESSION_MS = 30_000;

function recentlyAppliedUpdate() {
  try {
    const appliedAt = Number(localStorage.getItem(UPDATE_APPLIED_KEY));
    return Number.isFinite(appliedAt) && Date.now() - appliedAt < UPDATE_SUPPRESSION_MS;
  } catch {
    return false;
  }
}

function rememberAppliedUpdate() {
  try {
    localStorage.setItem(UPDATE_APPLIED_KEY, String(Date.now()));
  } catch {
    // Storage can be unavailable in private browsing contexts.
  }
}

function applyWaitingUpdate(registration) {
  if (updateReloadRequested) return;
  updateReloadRequested = true;
  rememberAppliedUpdate();

  const reload = () => window.location.reload();
  if (navigator.serviceWorker?.addEventListener) {
    navigator.serviceWorker.addEventListener('controllerchange', reload, { once: true });
  }

  if (registration.waiting) {
    registration.waiting.postMessage({ type: 'SKIP_WAITING' });
  } else {
    reload();
  }
}

function monitorRegistration(registration) {
  let updateOffered = false;

  const offerUpdate = () => {
    if (
      updateOffered ||
      !navigator.serviceWorker.controller ||
      !registration.waiting ||
      recentlyAppliedUpdate()
    ) {
      return;
    }
    updateOffered = true;
    addToast({
      title: 'Update available',
      message: 'A new version of Windshift is ready.',
      variant: 'info',
      actionLabel: 'Reload',
      duration: 0,
      clickable: true,
      onClick: () => applyWaitingUpdate(registration),
    });
  };

  const watchInstalling = () => {
    const installing = registration.installing;
    if (!installing) return;
    if (installing.state === 'installed') {
      offerUpdate();
      return;
    }
    installing.addEventListener?.('statechange', () => {
      if (installing.state === 'installed') offerUpdate();
    });
  };

  registration.addEventListener?.('updatefound', watchInstalling);
  watchInstalling();
  if (registration.waiting) queueMicrotask(offerUpdate);
}

/**
 * Register the mobile service worker. Idempotent — repeated calls return the
 * same in-flight/settled registration. Paths resolve against document.baseURI
 * so root and context-path deployments use the correct script and scope.
 *
 * @returns {Promise<ServiceWorkerRegistration|null>}
 */
export function registerMobileServiceWorker() {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) {
    return Promise.resolve(null);
  }
  if (registrationPromise) return registrationPromise;

  const swUrl = new URL('service-worker.js', document.baseURI).pathname;
  const scope = new URL('./', document.baseURI).pathname;

  registrationPromise = navigator.serviceWorker
    .register(swUrl, { scope })
    .then((registration) => {
      monitorRegistration(registration);
      return registration;
    })
    .catch((err) => {
      console.warn('[mobile] service worker registration failed:', err);
      registrationPromise = null;
      return null;
    });

  return registrationPromise;
}

/** Test helper: allow isolated registration tests without reloading the module. */
export function resetServiceWorkerRegistrationForTests() {
  registrationPromise = null;
  updateReloadRequested = false;
}
