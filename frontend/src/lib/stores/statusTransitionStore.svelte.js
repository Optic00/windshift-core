import { SvelteMap } from 'svelte/reactivity';
import { api } from '../api.js';
import { BaseCacheStore } from './BaseCacheStore.svelte.js';

const TTL_MS = 10 * 60 * 1000; // 10 minutes

/**
 * Caches available status transitions by (itemTypeId, statusId) instead of per-item.
 * Transitions depend only on item type + current status (via workflow config),
 * so a board with 100 items and 5 statuses across 2 item types needs ~10 fetches, not 100.
 *
 * Survives view switches (singleton store, not component-local).
 */
class StatusTransitionStore extends BaseCacheStore {
  // Transition lookups happen directly while board menus and drop targets
  // render. Keep this cache reactive so an asynchronous preload immediately
  // refreshes those consumers instead of waiting for an unrelated item update.
  _cache = new SvelteMap();

  _cacheKey(itemTypeId, statusId) {
    return `${itemTypeId}:${statusId}`;
  }

  /**
   * Synchronous lookup. Returns cached transitions or null if missing/expired.
   */
  get(itemTypeId, statusId) {
    if (!itemTypeId || !statusId) return null;
    const entry = this._cache.get(this._cacheKey(itemTypeId, statusId));
    if (!entry) return null;
    if (Date.now() - entry.fetchedAt > TTL_MS) return null;
    return entry.transitions;
  }

  /**
   * Synchronous validation check for drag-and-drop.
   */
  isValidTransition(itemTypeId, fromStatusId, toStatusId) {
    if (!fromStatusId || !toStatusId) return false;
    if (fromStatusId === toStatusId) return true;
    const transitions = this.get(itemTypeId, fromStatusId);
    if (!transitions) return false; // fail-safe: deny if unknown
    return transitions.some((t) => t.id === toStatusId);
  }

  /**
   * Preload the entire (itemTypeId, statusId) transition matrix for a workspace
   * in a single request, populating the cache for every pair. Preferred over
   * preloadForItems on board/list views: it collapses what was up to N per-pair
   * requests into one. In-flight requests for the same workspace are deduped.
   */
  async preloadForWorkspace(workspaceId) {
    if (!workspaceId) return;
    const pendingKey = `ws:${workspaceId}`;
    if (this._pending.has(pendingKey)) return this._pending.get(pendingKey);
    const generation = this._generation;
    const scopedWorkspaceId = this.workspaceId;

    const promise = (async () => {
      try {
        const result = await api.workspaces.getTransitionMatrix(workspaceId);
        if (generation !== this._generation || scopedWorkspaceId !== this.workspaceId) return;
        const matrix = result?.transitions || {};
        const fetchedAt = Date.now();
        for (const [key, transitions] of Object.entries(matrix)) {
          this._cache.set(key, { transitions: transitions || [], fetchedAt });
        }
      } catch (err) {
        console.error(
          `StatusTransitionStore: failed to preload matrix for workspace ${workspaceId}`,
          err
        );
      } finally {
        if (this._pending.get(pendingKey) === promise) this._pending.delete(pendingKey);
      }
    })();

    this._pending.set(pendingKey, promise);
    return promise;
  }

  /**
   * Batch-preload transitions for a list of items.
   * Groups by unique (itemTypeId, statusId), fetches only uncached pairs
   * using one representative item per pair.
   */
  async preloadForItems(items) {
    if (!items || items.length === 0) return;

    // If a workspace-wide matrix preload is in flight, defer to it — it will
    // populate every pair, leaving this call a no-op (avoids racing the matrix
    // with per-pair fetches on first board load).
    const wsPending = this.workspaceId ? this._pending.get(`ws:${this.workspaceId}`) : null;
    if (wsPending) await wsPending;

    // Group items by unique (itemTypeId, statusId), pick one representative per group
    const representatives = new Map();
    for (const item of items) {
      if (!item.item_type_id || !item.status_id) continue;
      const key = this._cacheKey(item.item_type_id, item.status_id);

      // Skip if already cached and not expired
      const existing = this._cache.get(key);
      if (existing && Date.now() - existing.fetchedAt <= TTL_MS) continue;

      // Skip if we already picked a representative for this pair
      if (representatives.has(key)) continue;

      representatives.set(key, item);
    }

    if (representatives.size === 0) return;

    // Fetch all uncached pairs concurrently, deduplicating in-flight requests
    const fetchPromises = [];
    for (const [key, item] of representatives) {
      fetchPromises.push(this._fetchForItem(key, item));
    }

    await Promise.all(fetchPromises);
  }

  /** @private */
  async _fetchForItem(cacheKey, item) {
    // Deduplicate: if already in-flight for this key, wait on existing promise
    if (this._pending.has(cacheKey)) {
      return this._pending.get(cacheKey);
    }

    const generation = this._generation;
    const scopedWorkspaceId = this.workspaceId;
    const promise = (async () => {
      try {
        const result = await api.items.getAvailableStatusTransitions(item.id);
        if (generation !== this._generation || scopedWorkspaceId !== this.workspaceId) return [];
        const transitions = result.available_transitions || [];
        this._cache.set(cacheKey, { transitions, fetchedAt: Date.now() });
        return transitions;
      } catch (err) {
        console.error(
          `StatusTransitionStore: failed to fetch transitions for key ${cacheKey}`,
          err
        );
        return [];
      } finally {
        if (this._pending.get(cacheKey) === promise) this._pending.delete(cacheKey);
      }
    })();

    this._pending.set(cacheKey, promise);
    return promise;
  }

  /**
   * Clear all cached transitions (e.g. after workflow configuration changes).
   * Uses inherited invalidateAll() from BaseCacheStore.
   */

  /**
   * Full reset: clear cache and workspace scope.
   * Uses inherited reset() from BaseCacheStore.
   */
}

export const statusTransitionStore = new StatusTransitionStore();
