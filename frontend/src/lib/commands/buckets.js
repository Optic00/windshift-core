/**
 * Command buckets — the coarse grouping that drives display order in the
 * command palette. Within a bucket, commands are ordered by text score and
 * then by insertion order from their provider.
 *
 * Order here is the *default* display order. `bucketOrder()` may promote
 * search-results above global-navigation when the query looks like an item
 * key (e.g. `ABC-123`).
 */

/** @type {Readonly<Record<string, string>>} */
export const BUCKET = Object.freeze({
  ITEM_ACTIONS: 'item-actions',
  WORKSPACE_ACTIONS: 'workspace-actions',
  WORKSPACE_NAVIGATION: 'workspace-navigation',
  MODULE_ACTIONS: 'module-actions',
  CREATE: 'create',
  ADMIN: 'admin',
  SEARCH_RESULTS: 'search-results',
  GLOBAL_NAVIGATION: 'global-navigation',
  SYSTEM: 'system',
});

const DEFAULT_ORDER = [
  BUCKET.ITEM_ACTIONS,
  BUCKET.WORKSPACE_ACTIONS,
  BUCKET.WORKSPACE_NAVIGATION,
  BUCKET.MODULE_ACTIONS,
  BUCKET.CREATE,
  BUCKET.ADMIN,
  BUCKET.GLOBAL_NAVIGATION,
  BUCKET.SEARCH_RESULTS,
  BUCKET.SYSTEM,
];

const ITEM_KEY_RE = /^[A-Z][A-Z0-9]*-\d+$/i;

/**
 * Return the rank index (lower = earlier) for a bucket given the query.
 * Item-key-shaped queries promote search-results above global-navigation.
 *
 * @param {string} bucket
 * @param {string} query
 * @returns {number}
 */
export function bucketRank(bucket, query) {
  let order = DEFAULT_ORDER;
  if (query && ITEM_KEY_RE.test(query.trim())) {
    order = DEFAULT_ORDER.slice();
    const search = order.indexOf(BUCKET.SEARCH_RESULTS);
    const global = order.indexOf(BUCKET.GLOBAL_NAVIGATION);
    if (search > global) {
      order.splice(search, 1);
      order.splice(global, 0, BUCKET.SEARCH_RESULTS);
    }
  }
  const idx = order.indexOf(bucket);
  return idx === -1 ? order.length : idx;
}

export const BUCKET_LABELS = Object.freeze({
  [BUCKET.ITEM_ACTIONS]: 'Item',
  [BUCKET.WORKSPACE_ACTIONS]: 'Workspace',
  [BUCKET.WORKSPACE_NAVIGATION]: 'Workspace',
  [BUCKET.MODULE_ACTIONS]: 'Tools',
  [BUCKET.CREATE]: 'Create',
  [BUCKET.ADMIN]: 'Admin',
  [BUCKET.SEARCH_RESULTS]: 'Search',
  [BUCKET.GLOBAL_NAVIGATION]: 'Navigation',
  [BUCKET.SYSTEM]: 'System',
});

// Per-bucket cap is set so empty-query palette renders all workspace views
// (up to ~12) and global nav items (up to ~10). Total cap keeps the list
// scannable without burying admin / system commands.
export const PER_BUCKET_CAP = 8;
export const TOTAL_CAP = 20;
