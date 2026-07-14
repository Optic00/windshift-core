let registrationPromise = null;

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

  registrationPromise = navigator.serviceWorker.register(swUrl, { scope }).catch((err) => {
    console.warn('[mobile] service worker registration failed:', err);
    registrationPromise = null;
    return null;
  });

  return registrationPromise;
}

/** Test helper: allow isolated registration tests without reloading the module. */
export function resetServiceWorkerRegistrationForTests() {
  registrationPromise = null;
}
