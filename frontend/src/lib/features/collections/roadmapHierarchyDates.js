const DATE_PATTERN = /^(\d{4})-(\d{2})-(\d{2})/;

function dateOnly(value) {
  if (!value) return null;
  const match = String(value).match(DATE_PATTERN);
  return match ? `${match[1]}-${match[2]}-${match[3]}` : null;
}

function sourceRange(item) {
  return {
    startDate: dateOnly(item.start_date),
    endDate: dateOnly(item.end_date),
    adjusted: false,
    summary: false,
  };
}

function bounds(range) {
  const start = range.startDate || range.endDate;
  const end = range.endDate || range.startDate;
  return start || end ? { start, end } : null;
}

function clamp(value, minimum, maximum) {
  if (!value) return null;
  if (minimum && value < minimum) return minimum;
  if (maximum && value > maximum) return maximum;
  return value;
}

function clampToParent(range, parentRange) {
  const parentBounds = bounds(parentRange);
  if (!parentBounds || !bounds(range)) return range;

  const startDate = clamp(range.startDate, parentBounds.start, parentBounds.end);
  const endDate = clamp(range.endDate, parentBounds.start, parentBounds.end);
  return {
    ...range,
    startDate,
    endDate,
    adjusted: startDate !== range.startDate || endDate !== range.endDate,
  };
}

function hierarchy(items) {
  const byId = new Map();
  const children = new Map();
  for (const item of items || []) {
    if (!item?.id || byId.has(Number(item.id))) continue;
    byId.set(Number(item.id), item);
  }
  for (const item of byId.values()) {
    const parentId = Number(item.parent_id);
    if (!Number.isFinite(parentId) || !byId.has(parentId)) continue;
    const siblings = children.get(parentId) || [];
    siblings.push(Number(item.id));
    children.set(parentId, siblings);
  }
  for (const siblings of children.values()) siblings.sort((a, b) => a - b);
  return { byId, children };
}

function rollupProjection(items) {
  const { byId, children } = hierarchy(items);
  const projected = new Map();
  const visiting = new Set();

  function visit(id) {
    if (projected.has(id)) return projected.get(id);
    const item = byId.get(id);
    const ownRange = sourceRange(item);
    if (visiting.has(id)) return ownRange;

    visiting.add(id);
    const descendantBounds = [];
    for (const childId of children.get(id) || []) {
      const childBounds = bounds(visit(childId));
      if (childBounds) descendantBounds.push(childBounds);
    }
    visiting.delete(id);

    let range = ownRange;
    if (descendantBounds.length > 0) {
      range = {
        startDate: descendantBounds.map((entry) => entry.start).sort()[0],
        endDate: descendantBounds
          .map((entry) => entry.end)
          .sort()
          .at(-1),
        adjusted: true,
        summary: true,
      };
    }
    projected.set(id, range);
    return range;
  }

  for (const id of byId.keys()) visit(id);
  return projected;
}

function rolldownProjection(items) {
  const { byId, children } = hierarchy(items);
  const projected = new Map();
  const visited = new Set();

  function visit(id, parentRange = null) {
    if (visited.has(id)) return;
    visited.add(id);
    const ownRange = sourceRange(byId.get(id));
    const range = parentRange ? clampToParent(ownRange, parentRange) : ownRange;
    projected.set(id, range);
    for (const childId of children.get(id) || []) visit(childId, range);
  }

  for (const item of byId.values()) {
    const parentId = Number(item.parent_id);
    if (!Number.isFinite(parentId) || !byId.has(parentId)) visit(Number(item.id));
  }
  for (const id of byId.keys()) visit(id);
  return projected;
}

export function projectHierarchyDates(items, mode) {
  if (mode === 'rollup') return rollupProjection(items);
  if (mode === 'rolldown') return rolldownProjection(items);
  return new Map((items || []).map((item) => [Number(item.id), sourceRange(item)]));
}

function changedDateFields(before, after) {
  const fields = {};
  if (before.startDate !== after.startDate) fields.start_date = after.startDate;
  if (before.endDate !== after.endDate) fields.end_date = after.endDate;
  return fields;
}

function mergePatch(patches, itemId, fields) {
  if (Object.keys(fields).length === 0) return;
  const current = patches.get(itemId) || {};
  patches.set(itemId, { ...current, ...fields });
}

export function buildHierarchyDatePatches({ items, editedItemId, fields, mode }) {
  const editedId = Number(editedItemId);
  const cloned = (items || []).map((item) => ({ ...item }));
  const edited = cloned.find((item) => Number(item.id) === editedId);
  if (!edited) return [];

  const requested = {};
  if (Object.hasOwn(fields, 'start_date')) requested.start_date = dateOnly(fields.start_date);
  if (Object.hasOwn(fields, 'end_date')) requested.end_date = dateOnly(fields.end_date);
  Object.assign(edited, requested);

  const patches = new Map();
  mergePatch(patches, editedId, requested);
  if (mode !== 'rollup' && mode !== 'rolldown') {
    return [...patches.entries()].map(([item_id, set]) => ({ item_id, set }));
  }

  const { byId, children } = hierarchy(cloned);
  const projected = projectHierarchyDates(cloned, mode);
  const affected = new Set();

  if (mode === 'rollup') {
    let current = byId.get(editedId)?.parent_id;
    while (current != null && byId.has(Number(current)) && !affected.has(Number(current))) {
      affected.add(Number(current));
      current = byId.get(Number(current))?.parent_id;
    }
  } else {
    const stack = [...(children.get(editedId) || [])];
    while (stack.length > 0) {
      const id = stack.pop();
      if (affected.has(id)) continue;
      affected.add(id);
      stack.push(...(children.get(id) || []));
    }
  }

  for (const id of affected) {
    const item = byId.get(id);
    const projection = projected.get(id);
    if (!item || !projection) continue;
    mergePatch(patches, id, changedDateFields(sourceRange(item), projection));
  }

  return [...patches.entries()]
    .sort(([left], [right]) => left - right)
    .map(([item_id, set]) => ({ item_id, set }));
}
