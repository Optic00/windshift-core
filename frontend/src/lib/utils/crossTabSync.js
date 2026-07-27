// Broadcasts local work-item mutations so other tabs can refresh active
// collection views. Polling still covers server-side changes.

const CHANNEL_NAME = 'windshift-work-items';

/** Unique-per-tab id used to identify the origin of a broadcast. */
export const tabId =
  typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : String(Math.random());

let channel = null;
let initialized = false;

/** @returns {BroadcastChannel | null} */
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
 * Best-effort mutation broadcast.
 * @param {{ type?: string, itemId?: number|string, parentId?: number|string|null }} [detail]
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
    // Broadcasting must not break the originating mutation.
  }
}

/**
 * Installs one debounced listener for other tabs' mutations.
 * @param {{ refreshCollectionDeltas?: () => Promise<void>|void, debounceMs?: number }} [handlers]
 * @returns {() => void} disposer
 */
export function initCrossTabSync(handlers = {}) {
  if (initialized) return () => {};
  initialized = true;

  const ch = getChannel();
  if (!ch) {
    // Preserve a uniform disposer when BroadcastChannel is unavailable.
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

    // Coalesce bulk mutations into one refresh.
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
