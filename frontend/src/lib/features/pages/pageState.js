/**
 * Merge a page update response into the currently displayed page.
 *
 * The page update endpoint returns the persisted page row, while labels are
 * hydrated separately for detail responses. Keep those hydrated labels when
 * an update response does not include them so autosaves and appearance
 * changes do not temporarily clear the label row.
 */
export function mergePageUpdate(currentPage, updatedPage, contentOverride) {
  return {
    ...updatedPage,
    labels: updatedPage?.labels ?? currentPage?.labels ?? [],
    ...(contentOverride === undefined ? {} : { content: contentOverride }),
  };
}
