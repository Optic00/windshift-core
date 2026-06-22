import { navigate } from '../../router.js';

/**
 * Default window for completed items in dashboard task lists: items that
 * entered a done status more than this many days ago are hidden. Mirrors the
 * per-workspace TodoList "Done range" default (WI-473).
 */
export const DEFAULT_DONE_RANGE_DAYS = 7;

/**
 * ISO date (YYYY-MM-DD) `days` ago, for the backend `completed_since` filter.
 * Matches the cutoff format TodoList sends so the server treats both the same.
 * @param {number} [days]
 */
export function completedSinceCutoff(days = DEFAULT_DONE_RANGE_DAYS) {
  const d = new Date();
  d.setDate(d.getDate() - days);
  return d.toISOString().slice(0, 10);
}

/**
 * Query params for "items assigned to me and not yet completed", newest first.
 * Single source of truth for this QL contract — reused by the dashboard widget
 * and the mobile My Work view so the two can't drift.
 * @param {number|string} userId
 */
export function assignedToMeQuery(userId) {
  return {
    ql: `assignee_id = ${userId} AND status_completed = false`,
    limit: 30,
    order_by: 'updated_at',
  };
}

export function normalizeTaskResponse(response, maxItems = 6) {
  const raw = Array.isArray(response) ? response : (response?.items ?? []);
  const active = raw
    .filter((i) => i?.id)
    .map((i) => ({
      ...i,
      dueDate: i.due_date ? new Date(i.due_date) : null,
    }));
  active.sort((a, b) => {
    if (a.dueDate && b.dueDate) return a.dueDate - b.dueDate;
    if (a.dueDate) return -1;
    if (b.dueDate) return 1;
    return 0;
  });
  return active.slice(0, maxItems);
}

export function openTask(task) {
  navigate(`/workspaces/${task.workspace_id}/items/${task.id}`);
}
