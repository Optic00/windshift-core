export const LAZY_LOAD_SESSION_CHECK_TIMEOUT_MS = 5_000;

/**
 * Revalidate authentication after a component import fails.
 *
 * Dynamic import errors do not expose the HTTP status of the chunk request, so
 * a small authenticated request is the only reliable way to distinguish an
 * expired browser session from a stale chunk or a transient network failure.
 */
export async function hasSessionExpired(checkSession) {
  try {
    await checkSession({ timeout: LAZY_LOAD_SESSION_CHECK_TIMEOUT_MS });
    return false;
  } catch (error) {
    return error?.status === 401;
  }
}
