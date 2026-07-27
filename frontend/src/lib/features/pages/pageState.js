/** Merge an update while preserving separately hydrated labels. */
export function mergePageUpdate(currentPage, updatedPage, contentOverride) {
  return {
    ...updatedPage,
    labels: updatedPage?.labels ?? currentPage?.labels ?? [],
    ...(contentOverride === undefined ? {} : { content: contentOverride }),
  };
}
