/**
 * Cross-tab work-item freshness.
 *
 * Same-user, same-browser multi-tab support for active collection board/list
 * views: when one Windshift tab performs a local item mutation (create /
 * update / status transition / reorder / delete), it broadcasts a notice over
 * a `BroadcastChannel` so other open tabs refresh the views they currently
 * show. This complements — and does not replace — the adaptive poller that
 * catches server-side changes (other users, background jobs, agent runs).
 *
 * Browser `BroadcastChannel` excludes the posting context from its own
 * `message` events, so a tab only ever reacts to *other* tabs. We still stamp
 * an `origin` id so listeners (and tests) can ignore self-originated echoes
 * if they ever need to (e.g. when relayed through storage events).
 */

const CHANNEL_NAME = 'windshift-work-items';

/** Unique-per-tab id used to identify the origin of a broadcast. */
export const tabId =
  typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : String(Math.random());

let channel = null;
let initialized = false;

/**
 * Lazily create (and memoize) the BroadcastChannel. Returns null when the API
 * is unavailable (older browsers, jsdom without a polyfill, non-window envs).
 *
 * @returns {BroadcastChannel | null}
 */
function getChannel() {
  if (channel) return channel;
  if (typeof BroadcastChannel === 'undefined') return null;
  try {
    channel = new BroadcastChannel(CHANNEL_NAME);
  } catch {
    channel = null;
  }
  return channel;
}

/**
 * Broadcast a work-item mutation notice to other open tabs.
 *
 * Safe to call from anywhere; no-ops when BroadcastChannel is unavailable so
 * callers (e.g. the items API client) never need to feature-detect.
 *
 * @param {{ type?: string, itemId?: number|string, parentId?: number|string|null }} [detail]
 *   `type` is a coarse mutation category (create/update/transition/reorder/delete);
 *   `itemId` is the affected item when known; `parentId` lets item-detail views
 *   refresh child lists for the parent of a created/changed item.
 * @returns {void}
 */
export function notifyItemMutation(detail = {}) {
  const ch = getChannel();
  if (!ch) return;
  try {
    ch.postMessage({
      type: detail.type || 'update',
      itemId: detail.itemId ?? null,
      parentId: detail.parentId ?? null,
      origin: tabId,
      ts: Date.now(),
    });
  } catch {
    // Swallow — broadcasting is best-effort freshness; a failed post must not
    // break the mutation that triggered it.
  }
}

/**
 * Install the cross-tab listener. Idempotent: calling more than once (e.g.
 * across hot reloads) will not register duplicate handlers.
 *
 * On a message from another tab we refresh the active collection board/list
 * view via the injected `refreshCollectionDeltas` handler — the same cheap
 * delta-patch path the adaptive poller uses. This is deliberately scoped to
 * active collection views (per the WI-101 story) and intentionally does NOT
 * re-dispatch the same-tab `refresh-work-items` event, which is a bespoke
 * single-item append fast-path that would race against the delta refresh.
 *
 * Foreign messages are coalesced with a trailing debounce: a single bulk
 * operation in another tab (e.g. completing an iteration, which fires one
 * mutation per item) would otherwise broadcast N notices, and each one drives
 * a full board reload here (board views always take the full-refresh branch in
 * refreshDeltas). Debouncing collapses the burst into one refresh.
 *
 * @param {{ refreshCollectionDeltas?: () => Promise<void>|void, debounceMs?: number }} [handlers]
 *   Handlers are injected so this module stays free of a hard dependency on
 *   the collection store (avoids import cycles + keeps it trivially testable).
 *   `debounceMs` (default 200) is the trailing-debounce window for coalescing.
 * @returns {() => void} disposer that removes the listener and tears down the channel.
 */
export function initCrossTabSync(handlers = {}) {
  if (initialized) return () => {};
  initialized = true;

  const ch = getChannel();
  if (!ch) {
    // Nothing to listen on — return a no-op disposer so callers are uniform.
    return () => {
      initialized = false;
    };
  }

  const debounceMs = handlers.debounceMs ?? 200;
  let refreshTimer = null;

  const onMessage = (/** @type {MessageEvent} */ event) => {
    const data = event?.data;
    if (!data || data.origin === tabId) return; // ignore self echoes
    if (typeof handlers.refreshCollectionDeltas !== 'function') return;

    // Trailing debounce: a burst of broadcasts (bulk mutation in another tab)
    // collapses into a single refresh once the notices stop arriving.
    if (refreshTimer !== null) clearTimeout(refreshTimer);
    refreshTimer = setTimeout(() => {
      refreshTimer = null;
      Promise.resolve(handlers.refreshCollectionDeltas()).catch((err) =>
        console.warn('[crossTabSync] refreshCollectionDeltas failed:', err)
      );
    }, debounceMs);
  };

  ch.addEventListener('message', onMessage);

  return () => {
    if (refreshTimer !== null) clearTimeout(refreshTimer);
    refreshTimer = null;
    ch.removeEventListener('message', onMessage);
    try {
      ch.close();
    } catch {
      /* ignore */
    }
    channel = null;
    initialized = false;
  };
}
