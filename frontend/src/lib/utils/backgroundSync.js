/**
 * Background polling is not useful while the browser has suspended the page
 * or knows it is offline. Skipping those requests also avoids a burst of
 * browser-level connection errors when a long-hidden tab wakes up.
 *
 * @param {{ document?: Document, navigator?: Navigator }} [environment]
 */
export function canRunBackgroundSync(environment = {}) {
  const documentRef = environment.document ?? globalThis.document;
  const navigatorRef = environment.navigator ?? globalThis.navigator;

  if (documentRef?.hidden || documentRef?.visibilityState === 'hidden') return false;
  if (navigatorRef?.onLine === false) return false;
  return true;
}

/**
 * Failures caused by connectivity changes or page teardown are expected
 * control flow for background refreshes. Other errors still deserve logging.
 *
 * @param {unknown} error
 */
export function isExpectedBackgroundSyncError(error) {
  const candidate = /** @type {{ name?: string, code?: string }} */ (error);
  return (
    candidate?.name === 'AbortError' ||
    candidate?.code === 'NETWORK_ERROR' ||
    candidate?.code === 'REQUEST_TIMEOUT'
  );
}

/**
 * Invoke a refresh callback as soon as a hidden/offline page is usable again.
 * The callback owns its own in-flight de-duplication.
 *
 * @param {() => void} callback
 * @param {{ document?: Document, navigator?: Navigator, window?: Window }} [environment]
 * @returns {() => void}
 */
export function onBackgroundSyncAvailable(callback, environment = {}) {
  const documentRef = environment.document ?? globalThis.document;
  const navigatorRef = environment.navigator ?? globalThis.navigator;
  const windowRef = environment.window ?? globalThis.window;

  const refreshIfAvailable = () => {
    if (canRunBackgroundSync({ document: documentRef, navigator: navigatorRef })) callback();
  };

  windowRef?.addEventListener('online', refreshIfAvailable);
  documentRef?.addEventListener('visibilitychange', refreshIfAvailable);

  return () => {
    windowRef?.removeEventListener('online', refreshIfAvailable);
    documentRef?.removeEventListener('visibilitychange', refreshIfAvailable);
  };
}
