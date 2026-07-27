/** Shared pure donut-chart helpers for iteration and milestone detail views. */

export const PROGRESS_CHART_RADIUS = 48;
export const PROGRESS_CHART_CIRCUMFERENCE = 2 * Math.PI * PROGRESS_CHART_RADIUS;

// Used when the backend hasn't supplied a category color.
export const PROGRESS_CHART_FALLBACK_COLORS = [
  '#22c55e',
  '#3b82f6',
  '#d1d5db',
  '#f97316',
  '#ec4899',
  '#8b5cf6',
];

/**
 * Clamp `value` to [0, 100] and round. Returns 0 for non-numeric input.
 */
export function formatPercent(value) {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return Math.min(100, Math.max(0, Math.round(value)));
  }
  return 0;
}

/**
 * Calculate completion from the item counts returned alongside a progress
 * report. Counts are the source of truth; `fallbackPercent` supports older or
 * partial responses that do not include them.
 */
export function calculatePercentComplete(completedItems, totalItems, fallbackPercent = 0) {
  const completed = Number(completedItems);
  const total = Number(totalItems);

  if (Number.isFinite(completed) && Number.isFinite(total) && total > 0) {
    return formatPercent((completed / total) * 100);
  }

  return formatPercent(fallbackPercent);
}

/**
 * Turn a status_breakdown array (`[{ category_name, category_color, item_count }, ...]`)
 * into SVG arc segments laid out around the donut.
 */
export function buildProgressSegments(breakdown, totalItems) {
  if (!breakdown || !totalItems || totalItems <= 0) return [];
  let offset = 0;
  return breakdown
    .filter((segment) => segment.item_count > 0)
    .map((segment, index) => {
      const fraction = segment.item_count / totalItems;
      const arcLength = Math.max(fraction * PROGRESS_CHART_CIRCUMFERENCE, 0);
      const dasharray = `${arcLength} ${PROGRESS_CHART_CIRCUMFERENCE}`;
      const segmentData = {
        ...segment,
        dasharray,
        offset,
        color:
          segment.category_color ||
          PROGRESS_CHART_FALLBACK_COLORS[index % PROGRESS_CHART_FALLBACK_COLORS.length],
      };
      offset -= arcLength;
      return segmentData;
    });
}
