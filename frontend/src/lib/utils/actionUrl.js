// Shared parsing of a notification's action URL — the desktop item deep link
// the backend's itemActionURL() emits (/workspaces/<id>/items/<itemId>). Both
// the desktop NotificationCard and the mobile NotificationsView derive from
// this single source so their behaviour stays in lockstep.
const ITEM_ROUTE = /\/items\/(\d+)(?:[/?#]|$)/;

// itemIdFromActionUrl extracts the numeric item id from an action URL, or
// returns null when the URL doesn't point at an item.
export function itemIdFromActionUrl(actionUrl) {
  const m = actionUrl?.match(ITEM_ROUTE);
  return m ? m[1] : null;
}

// mobileActionUrl rewrites a desktop item deep link to the mobile item-detail
// route (/m/items/<id>). Non-item URLs pass through unchanged; empty/missing
// values return null so callers can fall back to a generic route.
export function mobileActionUrl(actionUrl) {
  if (!actionUrl) return null;
  const id = itemIdFromActionUrl(actionUrl);
  return id ? `/m/items/${id}` : actionUrl;
}
