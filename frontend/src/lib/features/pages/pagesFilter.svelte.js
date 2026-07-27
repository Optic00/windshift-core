// pagesFilter shares session-only per-workspace sidebar labels without prop
// drilling; workspace changes reset writing-first defaults.

let activeWorkspaceId = $state(/** @type {number | null} */ (null));
let labelIds = $state(/** @type {Set<number>} */ (new Set()));

function ensureWorkspace(workspaceId) {
  const id = Number(workspaceId);
  if (activeWorkspaceId !== id) {
    activeWorkspaceId = id;
    labelIds = new Set();
  }
}

export const pagesFilter = {
  get activeWorkspaceId() {
    return activeWorkspaceId;
  },
  /** Read-only filtered label IDs; mutate through toggle/clear. */
  get labelIds() {
    return labelIds;
  },
  /** True iff at least one label filter is active. */
  get isActive() {
    return labelIds.size > 0;
  },
  toggle(workspaceId, labelId) {
    ensureWorkspace(workspaceId);
    const next = new Set(labelIds);
    if (next.has(labelId)) next.delete(labelId);
    else next.add(labelId);
    labelIds = next;
  },
  remove(workspaceId, labelId) {
    ensureWorkspace(workspaceId);
    if (!labelIds.has(labelId)) return;
    const next = new Set(labelIds);
    next.delete(labelId);
    labelIds = next;
  },
  clear(workspaceId) {
    ensureWorkspace(workspaceId);
    if (labelIds.size === 0) return;
    labelIds = new Set();
  },
  reset(workspaceId) {
    ensureWorkspace(workspaceId);
  },
};
