import { navigate } from '../../router.js';

/**
 * Query params for "items assigned to me and not yet completed", newest first.
 * Single source of truth for this QL contract — reused by the dashboard widget
 * and the mobile My Work view so the two can't drift.
 * @param {number} userId
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
