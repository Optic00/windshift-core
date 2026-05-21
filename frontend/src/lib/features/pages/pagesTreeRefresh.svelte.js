// pagesTreeRefresh is a one-bit signal the right-pane editor uses to ask
// the pages sidebar to reload its tree without owning the tree state
// itself. Bump the tick after any operation that changes the tree shape
// (archive, move, permissions change that revokes view), and the sidebar's
// $effect refetches.
//
// The store lives in a separate module so PagesView.svelte and
// PagesNavSidebar.svelte don't need a parent-child wiring path — they can
// communicate across the WorkspaceNavigation layout without prop drilling.

let tick = $state(0);

export const pagesTreeRefresh = {
  get tick() {
    return tick;
  },
  bump() {
    tick += 1;
  },
};
