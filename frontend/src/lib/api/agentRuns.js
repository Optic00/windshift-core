// API client for the coding-agent harness runs surface (WI-91 / WI-83).
// The runs endpoints back the workspace-admin "Agent runs" panel + the
// item-detail runs list. Event polling is the cheap shape — call
// listEventsAfter(runId, afterId) every few seconds and append to a
// local store, trimming what you don't need to render.

import { fetchAPI } from './core.js';

export const agentRuns = {
  /**
   * List the workspace's recent agent runs.
   * @param {number} workspaceId
   * @param {{ limit?: number, beforeId?: number }} [opts]
   */
  listForWorkspace: (workspaceId, opts = {}) => {
    const params = new URLSearchParams();
    if (opts.limit) params.set('limit', String(opts.limit));
    if (opts.beforeId) params.set('before_id', String(opts.beforeId));
    const qs = params.toString();
    return fetchAPI(`/workspaces/${workspaceId}/agent-runs${qs ? `?${qs}` : ''}`);
  },

  /**
   * List the runs triggered against one work item (newest first) — backs
   * the item-detail "Agent log" tab (WI-260).
   * @param {number} itemId
   * @param {{ limit?: number, beforeId?: number }} [opts]
   */
  listForItem: (itemId, opts = {}) => {
    const params = new URLSearchParams();
    if (opts.limit) params.set('limit', String(opts.limit));
    if (opts.beforeId) params.set('before_id', String(opts.beforeId));
    const qs = params.toString();
    return fetchAPI(`/items/${itemId}/agent-runs${qs ? `?${qs}` : ''}`);
  },

  /** Get a single run by id. */
  get: (runId) => fetchAPI(`/agent-runs/${runId}`),

  /**
   * Poll the run's event stream. `afterId` is the highest event id the
   * caller has already rendered; pass 0 on the first call to get the
   * full backlog.
   */
  listEventsAfter: (runId, afterId = 0, limit = 200) => {
    const params = new URLSearchParams({
      after_id: String(afterId),
      limit: String(limit),
    });
    return fetchAPI(`/agent-runs/${runId}/events?${params}`);
  },

  /** Cancel an in-flight run. Idempotent — returns canceled=false when
   * the run is already terminal. */
  cancel: (runId) =>
    fetchAPI(`/agent-runs/${runId}/cancel`, {
      method: 'POST',
    }),
};
