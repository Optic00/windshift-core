// Pure helpers for the board-card dependency/blocker hover summary.
// Extracted from DependencySummary.svelte so the direction-splitting logic
// is unit-testable without mounting the Svelte component.

// System link-type name that carries dependency semantics (forward:
// "depends on", reverse: "blocks"). Matched by name rather than a numeric id
// because link-type ids are workspace-specific.
export const DEPENDS_ON_NAME = 'Depends On';

/**
 * Partition a work item's merged outgoing+incoming link list into the two
 * dependency directions relative to the current item:
 *   - `blockers`: "Depends On" links where the item is the source → the item
 *     depends on those targets, so those targets block it.
 *   - `blocking`: "Depends On" links where the item is the target → other
 *     items depend on this one, so this item blocks them.
 *
 * Links of any other type (Implements, Relates To, Pages, etc.) are ignored.
 * Returns a normalised shape keyed off the link join fields
 * (the `source_`/`target_` columns) so callers don't need to re-derive
 * direction.
 *
 * @param {Array<object>} links - merged outgoing + incoming ItemLink objects.
 * @param {number|string} itemId - the current item's id.
 * @returns {{ blockers: Array<object>, blocking: Array<object> }}
 */
export function splitDependencies(links, itemId) {
  const id = Number(itemId);
  const blockers = [];
  const blocking = [];
  if (!Array.isArray(links)) return { blockers, blocking };

  for (const link of links) {
    if (!link || link.link_type_name !== DEPENDS_ON_NAME) continue;
    const isSource = Number(link.source_id) === id;
    const isTarget = Number(link.target_id) === id;

    if (isSource) {
      blockers.push({
        id: link.target_id,
        title: link.target_title,
        keyPrefix: link.target_workspace_key,
        itemNumber: link.target_item_number,
        statusName: link.target_status_name,
        statusColor: link.target_status_color,
        typeIcon: link.target_item_type_icon,
        typeColor: link.target_item_type_color,
      });
    } else if (isTarget) {
      blocking.push({
        id: link.source_id,
        title: link.source_title,
        keyPrefix: link.source_workspace_key,
        itemNumber: link.source_item_number,
        statusName: link.source_status_name,
        statusColor: link.source_status_color,
        typeIcon: link.source_item_type_icon,
        typeColor: link.source_item_type_color,
      });
    }
  }
  return { blockers, blocking };
}

/**
 * Build the display key for a linked item (e.g. "WI-74"), preferring the
 * workspace-scoped number and falling back to the raw id. Mirrors how the
 * item-detail links surface keys.
 *
 * @param {{ keyPrefix?: string, itemNumber?: number|string, id?: number|string }} linked
 * @returns {string}
 */
export function dependencyKey(linked) {
  if (!linked) return '';
  const prefix = linked.keyPrefix || 'WORK';
  return `${prefix}-${linked.itemNumber ?? linked.id}`;
}
