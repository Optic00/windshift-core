// Shares the active item SSE state so independent pollers pause during live
// updates. One open detail view makes a single item/connection pair sufficient.
let activeItemId = $state(null);
let connected = $state(false);

export const itemLiveUpdates = {
  // Whether pollers should defer to this item's stream.
  isLive(itemId) {
    return connected && itemId != null && Number(activeItemId) === Number(itemId);
  },
  // Record the active stream state.
  set(itemId, isConnected) {
    activeItemId = itemId == null ? null : Number(itemId);
    connected = isConnected;
  },
  // Clear only the matching active stream.
  clear(itemId) {
    if (itemId == null || Number(activeItemId) === Number(itemId)) {
      connected = false;
      activeItemId = null;
    }
  },
};
