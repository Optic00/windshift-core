// pagesFocusTitle is a transient signal that tells PagesView to focus
// (and select) the title input the next time it renders the page with the
// matching id. Set when the sidebar creates a new page from the + button
// so the user can type the real title immediately without an extra click.
//
// `pageId` is the id we want focused; `tick` lets PagesView's $effect run
// even when the user clicks + repeatedly with the same selected page (the
// id would match the previous focus target).

let pageId = $state(null);
let tick = $state(0);

export const pagesFocusTitle = {
  get pageId() {
    return pageId;
  },
  get tick() {
    return tick;
  },
  request(id) {
    pageId = id;
    tick += 1;
  },
  /** Clear after the focus has been honored so a remount doesn't refocus. */
  clear() {
    pageId = null;
  },
};
