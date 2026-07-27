// pagesFocusTitle requests title focus for new pages. tick retriggers the
// effect when repeated requests target the same page ID.

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
  /** Clear after focus so remounts do not refocus. */
  clear() {
    pageId = null;
  },
};
