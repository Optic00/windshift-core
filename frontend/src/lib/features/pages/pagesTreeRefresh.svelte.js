// pagesTreeRefresh is the signal the right-pane editor uses to ask the pages
// sidebar to update without owning the tree state itself. It has two channels:
//
//   tick   — bump() after any operation that changes the tree SHAPE (archive,
//            move, create, permissions change that revokes view). The sidebar's
//            $effect refetches the whole tree.
//   renamed — rename(id, title) when only a page's title changed. The sidebar
//            patches that one node in place — no refetch, no flash. A routine
//            content save touches neither channel (the tree shows only titles),
//            so typing in the body never re-renders the sidebar.
//
// The store lives in a separate module so PagesView.svelte and
// PagesNavSidebar.svelte don't need a parent-child wiring path — they can
// communicate across the WorkspaceNavigation layout without prop drilling.

let tick = $state(0);
let renamed = $state(null); // { id, title, seq } — last in-place rename

export const pagesTreeRefresh = {
  get tick() {
    return tick;
  },
  bump() {
    tick += 1;
  },
  get renamed() {
    return renamed;
  },
  // seq makes each call a distinct value so re-renaming to the same title
  // still re-triggers the sidebar's $effect.
  rename(id, title) {
    renamed = { id, title, seq: (renamed?.seq ?? 0) + 1 };
  },
};
