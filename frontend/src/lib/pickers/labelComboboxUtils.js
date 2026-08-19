// Shared value plumbing for label comboboxes: values are arrays of label
// names (or a legacy comma-separated string) while pickers work in ids.

// parseLabelValue converts the bound value (string[] | string) to an array of
// trimmed non-empty label names.
export function parseLabelValue(value) {
  if (!value) return [];
  if (Array.isArray(value)) return value;
  if (typeof value === 'string' && value.trim()) {
    return value
      .split(',')
      .map((name) => name.trim())
      .filter(Boolean);
  }
  return [];
}

// labelIdsForNames maps selected label names to their ids in the loaded
// label list; unknown names are dropped.
export function labelIdsForNames(names, labels) {
  return names.map((name) => labels.find((label) => label.name === name)?.id).filter(Boolean);
}
