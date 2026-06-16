import { notifyItemMutation } from '../utils/crossTabSync.js';
import { fetchAPI } from './core.js';
import { buildQueryString } from './utils.js';

/**
 * Wrap a mutating items API method so a successful call broadcasts a
 * cross-tab freshness notice to other open Windshift tabs. Failures are
 * surfaced unchanged (the original promise rejects) and never broadcast.
 *
 * @template {(...args: any[]) => Promise<any>} F
 * @param {F} fn
 * @param {string} type - coarse mutation category for the broadcast payload
 * @returns {F}
 */
function withCrossTabNotice(fn, type) {
  return /** @type {F} */ (
    async (...args) => {
      const result = await fn(...args);
      let itemId = null;
      if (typeof args[0] === 'number' || typeof args[0] === 'string') {
        itemId = args[0];
      } else if (result && typeof result === 'object' && result.id != null) {
        // create() takes a payload (no id arg) — pull it from the response.
        itemId = result.id;
      }
      notifyItemMutation({ type, itemId });
      return result;
    }
  );
}

export const items = {
  getAll: (filters = {}) => {
    return fetchAPI(`/items${buildQueryString(filters)}`);
  },
  get: (id) => fetchAPI(`/items/${id}`),
  getByKey: (workspaceKey, itemNumber) =>
    fetchAPI(
      `/workspaces/${encodeURIComponent(workspaceKey)}/items/${encodeURIComponent(itemNumber)}`
    ),
  getMany: (ids = []) => Promise.all([...new Set(ids)].map((id) => fetchAPI(`/items/${id}`))),
  getChanges: (filters = {}) => fetchAPI(`/items/changes${buildQueryString(filters)}`),
  create: withCrossTabNotice(
    (data) =>
      fetchAPI('/items', {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    'create'
  ),
  update: withCrossTabNotice(
    (id, data) =>
      fetchAPI(`/items/${id}`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    'update'
  ),
  // Perform a workflow status transition. Use this instead of passing
  // status_id to update() — the update endpoint rejects status_id so that
  // validator-mode and condition-mode workflow rules are always enforced.
  // Returns the updated item (unwrapped from the {item, old_status_id, ...} envelope).
  transition: withCrossTabNotice(async (id, toStatusId) => {
    const response = await fetchAPI(`/items/${id}/transition`, {
      method: 'POST',
      body: JSON.stringify({ to_status_id: toStatusId }),
    });
    return response.item;
  }, 'transition'),
  delete: withCrossTabNotice(
    (id) =>
      fetchAPI(`/items/${id}`, {
        method: 'DELETE',
      }),
    'delete'
  ),
  getDeleteInfo: (id) => fetchAPI(`/items/${id}/delete-info`),
  deleteCascade: withCrossTabNotice(
    (id) =>
      fetchAPI(`/items/${id}/cascade`, {
        method: 'DELETE',
      }),
    'delete'
  ),
  reparentChildren: (id, newParentId) =>
    fetchAPI(`/items/${id}/reparent-children`, {
      method: 'POST',
      body: JSON.stringify({ newParentId }),
    }),
  copy: withCrossTabNotice(
    (id) =>
      fetchAPI(`/items/${id}/copy`, {
        method: 'POST',
      }),
    'create'
  ),
  updateFracIndex: withCrossTabNotice(
    (id, data) =>
      fetchAPI(`/items/${id}/frac-index`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    'reorder'
  ),
  getBacklog: (
    workspaceId,
    ql = null,
    collectionId = null,
    /** @type {any} */ { page, limit, sub_ql } = {}
  ) => {
    const params = new URLSearchParams();
    if (collectionId) {
      params.append('collection_id', collectionId);
    } else if (workspaceId) {
      params.append('workspace_id', workspaceId);
    }
    if (ql) params.append('ql', ql);
    if (sub_ql) params.append('sub_ql', sub_ql);
    if (page) params.append('page', page);
    if (limit) params.append('limit', limit);
    return fetchAPI(`/items/backlog?${params}`);
  },
  getChildren: (itemId) => fetchAPI(`/items/${itemId}/children`),
  getAncestors: (itemId) => fetchAPI(`/items/${itemId}/ancestors`),
  getDescendants: (itemId, maxDepth = null) => {
    const params = maxDepth ? `?max_depth=${maxDepth}` : '';
    return fetchAPI(`/items/${itemId}/descendants${params}`);
  },
  getTimeRollup: (itemId, { maxDepth = 10 } = {}) =>
    fetchAPI(`/items/${itemId}/time-rollup?max_depth=${maxDepth}`),
  // Get available status transitions for a specific item based on workflow configuration
  getAvailableStatusTransitions: (itemId) =>
    fetchAPI(`/items/${itemId}/available-status-transitions`),
  analyzeTypeChange: (itemId, targetItemTypeId) =>
    fetchAPI(`/items/${itemId}/type-change-analysis?target_item_type_id=${targetItemTypeId}`),
  changeType: withCrossTabNotice(
    (itemId, data) =>
      fetchAPI(`/items/${itemId}/change-type`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    'update'
  ),
  // Get history of changes for an item
  getHistory: (itemId) => fetchAPI(`/items/${itemId}/history`),

  // Get items created in the last N days
  getRecentlyCreated: (workspaceId, days = 7) => {
    const sevenDaysAgo = new Date();
    sevenDaysAgo.setDate(sevenDaysAgo.getDate() - days);
    const createdSince = sevenDaysAgo.toISOString();
    const params = new URLSearchParams({
      workspace_id: workspaceId,
      created_since: createdSince,
    });
    return fetchAPI(`/items?${params}`);
  },

  // Watch/unwatch items
  addWatch: (id) =>
    fetchAPI(`/items/${id}/watch`, {
      method: 'POST',
    }),
  removeWatch: (id) =>
    fetchAPI(`/items/${id}/watch`, {
      method: 'DELETE',
    }),
  getWatchStatus: (id) => fetchAPI(`/items/${id}/watch`),

  // Personal tasks relationship
  getPersonalTasks: (itemId) => fetchAPI(`/items/${itemId}/personal-tasks`),
  unlinkPersonalTask: (itemId) =>
    fetchAPI(`/items/${itemId}/related-work-item`, {
      method: 'DELETE',
    }),
};
