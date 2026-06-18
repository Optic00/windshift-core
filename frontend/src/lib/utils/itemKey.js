/**
 * Build a work-item display key (`PROJ-123`) from an object carrying
 * workspace_key + workspace_item_number. Returns null when either is missing,
 * so callers can render conditionally. Accepts items, watched-item rows,
 * worklogs — anything with those two fields.
 *
 * @param {{ workspace_key?: string, workspace_item_number?: number } | null | undefined} obj
 * @returns {string | null}
 */
export function formatItemKey(obj) {
  if (obj?.workspace_key && obj?.workspace_item_number) {
    return `${obj.workspace_key}-${obj.workspace_item_number}`;
  }
  return null;
}
