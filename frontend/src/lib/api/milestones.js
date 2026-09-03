import { fetchAllV2Pages, fetchV2Data } from './core.js';
import { createCrudClient } from './createCrudClient.js';

export const milestoneCategories = createCrudClient('/milestone-categories');

function planningQuery(filters = {}) {
  const params = new URLSearchParams();
  if (filters.workspace_id != null) params.set('workspace_id', String(filters.workspace_id));
  else params.set('scope', 'global');
  for (const [key, value] of Object.entries(filters)) {
    if (
      !['workspace_id', 'include_global', 'is_global'].includes(key) &&
      value != null &&
      value !== ''
    ) {
      params.set(key, String(value));
    }
  }
  return params.toString();
}

async function listPlanning(path, filters = {}) {
  const local = fetchAllV2Pages(`${path}?${planningQuery(filters)}`);
  if (filters.workspace_id == null || filters.include_global === false) return local;
  const [localRows, globalRows] = await Promise.all([
    local,
    fetchAllV2Pages(`${path}?scope=global`),
  ]);
  return [...localRows, ...globalRows];
}

function planningBody(data) {
  const { is_global: isGlobal, ...body } = data;
  if (isGlobal) {
    delete body.workspace_id;
    body.scope = 'global';
  }
  return body;
}

function milestonePatch(data) {
  const { name, description, target_date, status, category_id } = data;
  return { name, description, target_date, status, category_id };
}

function iterationPatch(data) {
  const { name, description, start_date, end_date, status, type_id } = data;
  return { name, description, start_date, end_date, status, type_id };
}

export const milestones = {
  getAll: (filters = {}) => listPlanning('/milestones', filters),
  get: (id) => fetchV2Data(`/milestones/${id}`),
  create: (data) =>
    fetchV2Data('/milestones', { method: 'POST', body: JSON.stringify(planningBody(data)) }),
  update: (id, data) =>
    fetchV2Data(`/milestones/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/merge-patch+json' },
      body: JSON.stringify(milestonePatch(data)),
    }),
  delete: (id) => fetchV2Data(`/milestones/${id}`, { method: 'DELETE' }),
  getTestStatistics: (id) => fetchV2Data(`/milestones/${id}/test-statistics`),
  getTestStatisticsMany: (ids = []) =>
    fetchV2Data(`/milestones/test-statistics?ids=${[...new Set(ids)].join(',')}`),
  getProgress: (id) => fetchV2Data(`/milestones/${id}/progress`),
  release: (id, data, idempotencyKey) =>
    fetchV2Data(`/milestones/${id}/release`, {
      method: 'POST',
      headers: idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : undefined,
      body: JSON.stringify(data),
    }),
  reorder: (scope, orderedIds) => {
    return fetchV2Data('/milestones/reorder', {
      method: 'POST',
      body: JSON.stringify({
        ordered_ids: orderedIds,
        category_id: scope?.category_id ?? undefined,
        ...(scope?.is_global ? { scope: 'global' } : { workspace_id: scope?.workspace_id }),
      }),
    });
  },
};

export const iterationTypes = createCrudClient('/iteration-types');

export const iterations = {
  getAll: (filters = {}) => listPlanning('/iterations', filters),
  get: (id) => fetchV2Data(`/iterations/${id}`),
  create: (data) =>
    fetchV2Data('/iterations', { method: 'POST', body: JSON.stringify(planningBody(data)) }),
  update: (id, data) =>
    fetchV2Data(`/iterations/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/merge-patch+json' },
      body: JSON.stringify(iterationPatch(data)),
    }),
  delete: (id) => fetchV2Data(`/iterations/${id}`, { method: 'DELETE' }),
  getProgress: (id) => fetchV2Data(`/iterations/${id}/progress`),
  // Bulk progress for many iterations in one request, keyed by iteration id.
  // Replaces one getProgress() per iteration on the dashboard timeline.
  getProgressMany: (ids = []) =>
    fetchV2Data(`/iterations/progress?ids=${[...new Set(ids)].join(',')}`),
  getBurndown: (id) => fetchV2Data(`/iterations/${id}/burndown`),
  complete: (id, moveIncompleteToIterationId = null) =>
    fetchV2Data(`/iterations/${id}/complete`, {
      method: 'POST',
      body: JSON.stringify({
        move_incomplete_to_iteration_id: moveIncompleteToIterationId,
      }),
    }),
};
