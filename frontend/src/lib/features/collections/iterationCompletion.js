import { getStatusCategory } from '../../utils/statusColors.js';

/**
 * Return the iteration items whose status category is not marked completed.
 * The completion flag is authoritative; category names are user-configurable.
 */
export function getIncompleteIterationItems(items, statuses, statusCategories) {
  return items.filter(
    (item) => getStatusCategory(item.status_name, statuses, statusCategories)?.is_completed !== true
  );
}
