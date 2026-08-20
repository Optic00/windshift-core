import { notifyItemMutation } from '../utils/crossTabSync.js';
import { fetchAPI } from './core.js';
import { buildQueryString } from './utils.js';

// Item ids per GET /items/batch request. Kept under the server cap (500) and
// aligned with the links-batch chunk size to bound URL length.
const ITEM_BATCH_CHUNK = 200;

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

/**
 * @param {number|string} id
 * @param {RequestInit & { surface?: string }} [options]
 */
function fetchItemDetailSummary(id, options = {}) {
  const { surface, ...requestOptions } = options;
  return fetchAPI(
    `/items/${id}/detail-summary${surface ? `?surface=${encodeURIComponent(surface)}` : ''}`,
    requestOptions
  );
}

/**
 * @param {string} workspaceKey
 * @param {number|string} itemNumber
 * @param {RequestInit & { surface?: string }} [options]
 */
function fetchItemDetailSummaryByKey(workspaceKey, itemNumber, options = {}) {
  const { surface, ...requestOptions } = options;
  return fetchAPI(
    `/workspaces/${encodeURIComponent(workspaceKey)}/items/${encodeURIComponent(itemNumber)}/detail-summary${surface ? `?surface=${encodeURIComponent(surface)}` : ''}`,
    requestOptions
  );
}

function fetchBacklog(
  workspaceId,
  ql = null,
  collectionId = null,
  /** @type {any} */ { page, limit, sub_ql, omit_descriptions, include_watermark } = {}
) {
  const params = new URLSearchParams();
  if (collectionId) {
    params.append('collection_id', collectionId);
  } else if (workspaceId) {
    params.append('workspace_id', workspaceId);
  }
  if (ql) params.append('ql', ql);
  if (sub_ql) params.append('sub_ql', sub_ql);
  if (omit_descriptions) params.append('omit_descriptions', 'true');
  if (include_watermark) params.append('include_watermark', 'true');
  if (page) params.append('page', page);
  if (limit) params.append('limit', limit);
  return fetchAPI(`/items/backlog?${params}`);
}

async function fetchBacklogBoundary(workspaceId, collectionId, subQL, boundary) {
  const options = {
    page: 1,
    limit: 1,
    sub_ql: subQL || undefined,
    omit_descriptions: true,
  };

  for (let attempt = 0; attempt < 2; attempt++) {
    const firstPage = await fetchBacklog(workspaceId, null, collectionId, options);
    const firstItems = firstPage?.items ?? (Array.isArray(firstPage) ? firstPage : []);
    if (boundary === 'start' || firstItems.length === 0) return firstItems[0] ?? null;

    const total = firstPage?.pagination?.total ?? firstItems.length;
    if (total <= 1) return firstItems[0] ?? null;

    const lastPage = await fetchBacklog(workspaceId, null, collectionId, {
      ...options,
      page: total,
    });
    const lastItems = lastPage?.items ?? (Array.isArray(lastPage) ? lastPage : []);
    if (lastItems.length > 0) return lastItems[0];
  }

  return null;
}

export const items = {
  getAll: (filters = {}, requestOptions = {}) => {
    return fetchAPI(`/items${buildQueryString(filters)}`, requestOptions);
  },
  get: (id, requestOptions = {}) => fetchAPI(`/items/${id}`, requestOptions),
  getDetailSummary: fetchItemDetailSummary,
  getByKey: (workspaceKey, itemNumber, requestOptions = {}) =>
    fetchAPI(
      `/workspaces/${encodeURIComponent(workspaceKey)}/items/${encodeURIComponent(itemNumber)}`,
      requestOptions
    ),
  getDetailSummaryByKey: fetchItemDetailSummaryByKey,
  /**
   * Fetch many items in one (or a few) bulk requests instead of one
   * GET /items/{id} per id. Returns an array of full item-detail objects in
   * unspecified order; ids the caller can't view or that don't exist are
   * silently omitted (consumers patch loaded rows by id and no-op on the
   * rest). Chunked under the server's 500-id cap. Replaces the former
   * Promise.all(...map(id => /items/{id})) fan-out that could exhaust the
   * Postgres pool on a collection delta refresh.
   */
  getMany: async (ids = []) => {
    const unique = [...new Set(ids)];
    if (unique.length === 0) return [];
    const chunks = [];
    for (let i = 0; i < unique.length; i += ITEM_BATCH_CHUNK) {
      chunks.push(unique.slice(i, i + ITEM_BATCH_CHUNK));
    }
    const results = await Promise.all(
      chunks.map((chunk) => fetchAPI(`/items/batch?ids=${chunk.join(',')}`))
    );
    return results.flat();
  },
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
  // Atomically apply one field patch to up to 500 work items. The server
  // returns only changed items; unchanged retries produce no duplicate events.
  bulkUpdate: withCrossTabNotice(
    (itemIds, fields) =>
      fetchAPI('/items/bulk-update', {
        method: 'POST',
        body: JSON.stringify({ item_ids: itemIds, set: fields }),
      }),
    'update'
  ),
  bulkPatch: withCrossTabNotice(
    (patches) =>
      fetchAPI('/items/bulk-patch', {
        method: 'POST',
        body: JSON.stringify({ patches }),
      }),
    'update'
  ),
  getRoadmapHierarchyDates: (rootIds) =>
    fetchAPI('/items/roadmap-hierarchy-dates', {
      method: 'POST',
      body: JSON.stringify({ root_ids: rootIds }),
    }),
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
  previewWorkspaceMove: (id, data) =>
    fetchAPI(`/items/${id}/move-workspace/preview`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  moveWorkspace: withCrossTabNotice(
    (id, data) =>
      fetchAPI(`/items/${id}/move-workspace`, {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    'update'
  ),
  updateFracIndex: withCrossTabNotice(
    (id, data) =>
      fetchAPI(`/items/${id}/frac-index`, {
        method: 'PUT',
        body: JSON.stringify(data),
      }),
    'reorder'
  ),
  getBacklog: fetchBacklog,
  getBacklogBoundary: fetchBacklogBoundary,
  getChildren: (itemId, requestOptions = {}) =>
    fetchAPI(`/items/${itemId}/children`, requestOptions),
  getAncestors: (itemId, requestOptions = {}) =>
    fetchAPI(`/items/${itemId}/ancestors`, requestOptions),
  getDescendants: (itemId, maxDepth = null) => {
    const params = maxDepth ? `?max_depth=${maxDepth}` : '';
    return fetchAPI(`/items/${itemId}/descendants${params}`);
  },
  getTimeRollup: (itemId, { maxDepth = 10 } = {}) =>
    fetchAPI(`/items/${itemId}/time-rollup?max_depth=${maxDepth}`),
  // Get available status transitions for a specific item based on workflow configuration
  getAvailableStatusTransitions: (itemId, requestOptions = {}) =>
    fetchAPI(`/items/${itemId}/available-status-transitions`, requestOptions),
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
  getStatusDurations: (itemId, requestOptions = {}) =>
    fetchAPI(`/items/${itemId}/status-durations`, requestOptions),

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
  getWatchStatus: (id, requestOptions = {}) => fetchAPI(`/items/${id}/watch`, requestOptions),

  // Personal tasks relationship
  getPersonalTasks: (itemId, requestOptions = {}) =>
    fetchAPI(`/items/${itemId}/personal-tasks`, requestOptions),
  unlinkPersonalTask: (itemId) =>
    fetchAPI(`/items/${itemId}/related-work-item`, {
      method: 'DELETE',
    }),
};
