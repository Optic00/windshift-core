// Shared board-column derivation used by CollectionBoard (rendering) and
// collectionContext store (split fetching). Keeping both on one code path
// guarantees the store excludes exactly the statuses the board renders in
// its capped rightmost column.

/** Max cards rendered in the rightmost column when the board configuration
 * enables show_rightmost_column_last_50. Also the page size the store uses
 * for the separate rightmost-column fetch. */
export const RIGHTMOST_COLUMN_LIMIT = 50;

const CATEGORY_ORDER = {
  'To Do': 1,
  'In Progress': 2,
  Done: 3,
};

/**
 * Sorts statuses into board order: To Do -> In Progress -> Done categories,
 * alphabetical within a category.
 */
export function sortStatusesForBoard(statuses = []) {
  return statuses.slice().sort((a, b) => {
    const aOrder = CATEGORY_ORDER[a.category_name] || 999;
    const bOrder = CATEGORY_ORDER[b.category_name] || 999;
    if (aOrder !== bOrder) return aOrder - bOrder;
    return a.name.localeCompare(b.name);
  });
}

/**
 * Computes the board's display columns: configured columns when the board
 * configuration has any, otherwise one column per status in board order.
 */
export function buildDisplayColumns(boardConfig, statuses = []) {
  if (boardConfig?.columns?.length > 0) {
    return boardConfig.columns.slice().sort((a, b) => a.display_order - b.display_order);
  }
  return sortStatusesForBoard(statuses).map((status) => ({
    id: status.id,
    name: status.name,
    status_ids: [status.id],
    color: status.category_color,
    wip_limit: null,
    is_default_column: true,
  }));
}

/**
 * Returns the status IDs of the board's capped rightmost column, or null
 * when the cap doesn't apply (no config, cap disabled, or no resolvable
 * rightmost column).
 */
export function rightmostCapStatusIds(boardConfig, statuses = []) {
  if (!boardConfig?.show_rightmost_column_last_50) return null;
  const columns = buildDisplayColumns(boardConfig, statuses).filter(
    (col) => col.status_ids?.length > 0
  );
  const rightmost = columns[columns.length - 1];
  return rightmost?.status_ids?.length ? rightmost.status_ids : null;
}
