// Tracks whether the currently-open item-detail view has a healthy SSE stream
// (WI-484). Pollers that live in separate components — the work-item poller in
// ItemDetail/MobileItemDetail and the comments poller in Comments — read this to
// demote themselves while live updates are flowing, and resume automatically
// when the stream drops or is unsupported.
//
// Only one item-detail view is open at a time (modal on desktop, full screen on
// mobile), so a single (itemId, connected) pair is sufficient and avoids
// prop-drilling the connection state through intermediate components.
let activeItemId = $state(null);
let connected = $state(false);

export const itemLiveUpdates = {
  // isLive reports whether the given item currently has a live stream, so a
  // poller can gate on `!isLive(itemId)`.
  isLive(itemId) {
    return connected && itemId != null && Number(activeItemId) === Number(itemId);
  },
  // set records the stream state for the open item.
  set(itemId, isConnected) {
    activeItemId = itemId == null ? null : Number(itemId);
    connected = isConnected;
  },
  // clear resets state when a stream for itemId closes (no-op if a different
  // item has since become active).
  clear(itemId) {
    if (itemId == null || Number(activeItemId) === Number(itemId)) {
      connected = false;
      activeItemId = null;
    }
  },
};
