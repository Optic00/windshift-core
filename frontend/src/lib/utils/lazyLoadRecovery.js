export const LAZY_LOAD_SESSION_CHECK_TIMEOUT_MS = 5_000;
export const APP_SHELL_CHECK_TIMEOUT_MS = 5_000;

/** sessionStorage key holding the build a stale-chunk reload already targeted. */
export const STALE_BUILD_RELOAD_KEY = 'windshift-stale-build-reload';

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

/**
 * Identify a build by the hashed entry chunk its shell document loads
 * (`_app/index-<hash>.js`). Every build emits a different hash, and the
 * attribute is compared verbatim so a context-path deployment — where the
 * server rewrites root-relative attributes — still matches itself.
 */
function buildIdOf(doc) {
  return doc?.querySelector('script[type="module"][src]')?.getAttribute('src') ?? null;
}

async function fetchDeployedShell(fetchImpl, url) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), APP_SHELL_CHECK_TIMEOUT_MS);
  try {
    // `cache: 'reload'` skips the HTTP cache outright; the shell is served
    // no-cache, but a stale intermediary would defeat the whole check.
    const response = await fetchImpl(url, { cache: 'reload', signal: controller.signal });
    if (!response.ok) return null;
    return await response.text();
  } catch {
    return null;
  } finally {
    clearTimeout(timeout);
  }
}

/**
 * Detect that the server has been redeployed since this page loaded.
 *
 * A deploy replaces every content-hashed chunk, so an open tab asks for
 * filenames the new server no longer has and lazy imports start failing. The
 * shell document is the one URL with a stable name, so re-fetching it and
 * comparing entry chunks tells a redeploy apart from an offline client or a
 * genuinely broken chunk.
 *
 * @returns the deployed build id when it differs from the running one, else
 *   null — including when the check itself could not be completed.
 */
export async function findDeployedBuild({
  fetchImpl = globalThis.fetch,
  doc = globalThis.document,
  url = doc?.baseURI,
} = {}) {
  const running = buildIdOf(doc);
  if (!running || !fetchImpl || !url) return null;

  const html = await fetchDeployedShell(fetchImpl, url);
  if (!html) return null;

  // DOMParser does not execute or fetch anything it parses.
  const deployed = buildIdOf(new DOMParser().parseFromString(html, 'text/html'));
  return deployed && deployed !== running ? deployed : null;
}

function readReloadedBuild(storage) {
  try {
    return storage?.getItem(STALE_BUILD_RELOAD_KEY) ?? null;
  } catch {
    return null;
  }
}

function rememberReloadedBuild(storage, buildId) {
  try {
    storage?.setItem(STALE_BUILD_RELOAD_KEY, buildId);
  } catch {
    // Storage can be unavailable in hardened/private browser contexts. The
    // reload still helps; it just loses its loop guard.
  }
}

/**
 * Reload the page when a component import failed because the server was
 * redeployed under it. Reloading re-enters the SPA at the same route, so the
 * view the user asked for opens on the new build.
 *
 * The build reloaded into is recorded so a chunk that is broken for some other
 * reason cannot drive a reload loop: the second failure sees the same deployed
 * build and gives up. A later deploy has a different id and lifts the guard.
 *
 * @returns whether a reload was triggered.
 */
export async function reloadIfBuildChanged({
  storage = globalThis.sessionStorage,
  reload = () => globalThis.location.reload(),
  ...detection
} = {}) {
  const deployed = await findDeployedBuild(detection);
  if (!deployed || readReloadedBuild(storage) === deployed) return false;

  rememberReloadedBuild(storage, deployed);
  reload();
  return true;
}
