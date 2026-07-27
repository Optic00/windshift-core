// Views mapped to false stay put after creation; other views open item detail.
const viewNavigationConfig = {
  'workspace-board': false, // Stay on board after creating items
  'workspace-backlog': false, // Stay on backlog after creating items
};

/** Whether item creation should navigate from this view. */
export function shouldNavigateAfterCreate(viewName) {
  // Preserve the legacy navigate-by-default behavior.
  return viewNavigationConfig[viewName] ?? true;
}

/** Return a copy of the navigation configuration. */
export function getNavigationConfig() {
  return { ...viewNavigationConfig };
}
