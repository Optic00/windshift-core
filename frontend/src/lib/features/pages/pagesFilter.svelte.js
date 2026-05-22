// pagesFilter holds the per-workspace label filter for the pages sidebar.
// The state is session-only (no persistence): switching workspaces resets it,
// matching the writing-first defaults used for the editor mode toggle.
//
// Lives in a separate module so PagesNavSidebar.svelte and the (future)
// in-page filter button can share state without prop drilling. Mirrors the
// pagesTreeRefresh / pagesFocusTitle pattern.

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
  /**
   * Set of label ids currently being filtered. Caller treats this as
   * read-only — use the toggle/clear methods to mutate.
   */
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
