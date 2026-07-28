const DEFAULT_TOTAL_COLUMNS = 12;

function widgetWidth(widget, totalColumns) {
  const width = Number(widget?.width);
  if (!Number.isFinite(width)) return totalColumns;
  return Math.min(totalColumns, Math.max(1, Math.round(width)));
}

function widgetRows(widgets, totalColumns) {
  const rows = [];
  let row = [];
  let used = 0;

  for (const widget of widgets) {
    const width = widgetWidth(widget, totalColumns);
    if (row.length > 0 && used + width > totalColumns) {
      rows.push(row);
      row = [];
      used = 0;
    }
    row.push(widget);
    used += width;
    if (used === totalColumns) {
      rows.push(row);
      row = [];
      used = 0;
    }
  }
  if (row.length > 0) rows.push(row);
  return rows;
}

/**
 * Resolve the neighbouring widget and legal width range for a dashboard resize.
 * The right neighbour is preferred because the handle is on the target's right
 * edge. The left neighbour lets the final widget in a row use presets/keyboard
 * resizing without changing the row's total width.
 */
export function getDashboardResizeBounds(
  widgets,
  widgetId,
  getMinWidth,
  totalColumns = DEFAULT_TOTAL_COLUMNS
) {
  const row = widgetRows(widgets, totalColumns).find((candidate) =>
    candidate.some((widget) => widget.id === widgetId)
  );
  const targetIndex = row?.findIndex((widget) => widget.id === widgetId) ?? -1;
  if (!row || targetIndex < 0) return null;

  const target = row[targetIndex];
  const targetWidth = widgetWidth(target, totalColumns);
  const neighbour = row[targetIndex + 1] ?? row[targetIndex - 1] ?? null;
  if (!neighbour) {
    return {
      minWidth: targetWidth,
      maxWidth: targetWidth,
      neighbourId: null,
    };
  }

  const neighbourWidth = widgetWidth(neighbour, totalColumns);
  const pairWidth = targetWidth + neighbourWidth;
  const targetMin = Math.min(targetWidth, Math.max(1, getMinWidth(target.type)));
  const neighbourMin = Math.min(neighbourWidth, Math.max(1, getMinWidth(neighbour.type)));

  return {
    minWidth: targetMin,
    maxWidth: Math.max(targetMin, pairWidth - neighbourMin),
    neighbourId: neighbour.id,
  };
}

/**
 * Resize one widget and transfer the exact inverse delta to its neighbour.
 * Returning a new array keeps the store update atomic, so no intermediate
 * render can reflow a row above or below its existing column total.
 */
export function resizeDashboardWidgetRow(
  widgets,
  widgetId,
  requestedWidth,
  getMinWidth,
  totalColumns = DEFAULT_TOTAL_COLUMNS
) {
  const target = widgets.find((widget) => widget.id === widgetId);
  const bounds = getDashboardResizeBounds(widgets, widgetId, getMinWidth, totalColumns);
  if (!target || !bounds) {
    return { widgets, width: null, bounds };
  }

  const currentWidth = widgetWidth(target, totalColumns);
  const requested = Number.isFinite(Number(requestedWidth))
    ? Math.round(Number(requestedWidth))
    : currentWidth;
  const width = Math.min(bounds.maxWidth, Math.max(bounds.minWidth, requested));
  if (!bounds.neighbourId || width === currentWidth) {
    return { widgets, width: currentWidth, bounds };
  }

  const neighbour = widgets.find((widget) => widget.id === bounds.neighbourId);
  const neighbourWidth = widgetWidth(neighbour, totalColumns);
  const delta = width - currentWidth;
  const resized = widgets.map((widget) => {
    if (widget.id === widgetId) return { ...widget, width };
    if (widget.id === bounds.neighbourId) {
      return { ...widget, width: neighbourWidth - delta };
    }
    return widget;
  });

  return { widgets: resized, width, bounds };
}
