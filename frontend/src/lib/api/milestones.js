import { fetchAPI } from './core.js';
import { createCrudClient } from './createCrudClient.js';

export const milestoneCategories = createCrudClient('/milestone-categories');

// Update routes are scope-specific: workspace milestones live at
// /workspaces/{ws}/milestones/{id} (gated by workspace edit permission),
// global milestones at /global/milestones/{id} (gated by milestone.create).
// The helper picks the right URL from data.is_global / data.workspace_id so
// callers don't have to know about the route shape.
function milestoneUpdateUrl(id, data) {
  if (data?.is_global) return `/global/milestones/${id}`;
  if (data?.workspace_id == null) {
    throw new Error('milestone update requires workspace_id when is_global is false');
  }
  return `/workspaces/${data.workspace_id}/milestones/${id}`;
}

export const milestones = {
  ...createCrudClient('/milestones'),
  // Override update: the URL depends on data.is_global / data.workspace_id.
  update: (id, data) =>
    fetchAPI(milestoneUpdateUrl(id, data), {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  getTestStatistics: (id) => fetchAPI(`/milestones/${id}/test-statistics`),
  getTestStatisticsMany: (ids = []) =>
    fetchAPI(`/milestones/test-statistics?ids=${[...new Set(ids)].join(',')}`),
  getProgress: (id) => fetchAPI(`/milestones/${id}/progress`),
  release: (id, data, idempotencyKey) =>
    fetchAPI(`/milestones/${id}/release`, {
      method: 'POST',
      headers: idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : undefined,
      body: JSON.stringify(data),
    }),
  // Reorder is scope-specific: global milestones at /global/milestones/reorder,
  // workspace milestones at /workspaces/{ws}/milestones/reorder. Mirrors the
  // scope-split update routes. Pass { is_global, workspace_id } to pick the
  // URL; category_id optionally narrows to a per-category scope.
  reorder: (scope, orderedIds) => {
    const url = scope?.is_global
      ? '/global/milestones/reorder'
      : `/workspaces/${scope.workspace_id}/milestones/reorder`;
    return fetchAPI(url, {
      method: 'POST',
      body: JSON.stringify({
        ordered_ids: orderedIds,
        category_id: scope?.category_id ?? undefined,
      }),
    });
  },
};

export const iterationTypes = createCrudClient('/iteration-types');

// Iteration update — same scope rules as milestones (see milestoneUpdateUrl).
function iterationUpdateUrl(id, data) {
  if (data?.is_global) return `/global/iterations/${id}`;
  if (data?.workspace_id == null) {
    throw new Error('iteration update requires workspace_id when is_global is false');
  }
  return `/workspaces/${data.workspace_id}/iterations/${id}`;
}

export const iterations = {
  ...createCrudClient('/iterations'),
  // Override update: the URL depends on data.is_global / data.workspace_id.
  update: (id, data) =>
    fetchAPI(iterationUpdateUrl(id, data), {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  getProgress: (id) => fetchAPI(`/iterations/${id}/progress`),
  // Bulk progress for many iterations in one request, keyed by iteration id.
  // Replaces one getProgress() per iteration on the dashboard timeline.
  getProgressMany: (ids = []) =>
    fetchAPI(`/iterations/progress?ids=${[...new Set(ids)].join(',')}`),
  getBurndown: (id) => fetchAPI(`/iterations/${id}/burndown`),
  complete: (id, moveIncompleteToIterationId = null) =>
    fetchAPI(`/iterations/${id}/complete`, {
      method: 'POST',
      body: JSON.stringify({
        move_incomplete_to_iteration_id: moveIncompleteToIterationId,
      }),
    }),
};
