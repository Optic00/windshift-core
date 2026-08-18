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
 * edge. Unused columns are consumed before a neighbour is resized.
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
  const usedWidth = row.reduce((sum, widget) => sum + widgetWidth(widget, totalColumns), 0);
  const freeWidth = Math.max(0, totalColumns - usedWidth);
  const neighbour = row[targetIndex + 1] ?? row[targetIndex - 1] ?? null;
  if (!neighbour) {
    const targetMin = Math.min(targetWidth, Math.max(1, getMinWidth(target.type)));
    return {
      minWidth: targetMin,
      maxWidth: Math.max(targetMin, targetWidth + freeWidth),
      neighbourId: null,
    };
  }

  const neighbourWidth = widgetWidth(neighbour, totalColumns);
  const pairWidth = targetWidth + neighbourWidth;
  const targetMin = Math.min(targetWidth, Math.max(1, getMinWidth(target.type)));
  const neighbourMin = Math.min(neighbourWidth, Math.max(1, getMinWidth(neighbour.type)));

  return {
    minWidth: targetMin,
    maxWidth: Math.max(targetMin, pairWidth + freeWidth - neighbourMin),
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
  if (width === currentWidth) {
    return { widgets, width: currentWidth, bounds };
  }

  if (!bounds.neighbourId) {
    return {
      widgets: widgets.map((widget) => (widget.id === widgetId ? { ...widget, width } : widget)),
      width,
      bounds,
    };
  }

  const neighbour = widgets.find((widget) => widget.id === bounds.neighbourId);
  const neighbourWidth = widgetWidth(neighbour, totalColumns);
  const delta = width - currentWidth;
  const row = widgetRows(widgets, totalColumns).find((candidate) =>
    candidate.some((widget) => widget.id === widgetId)
  );
  const usedWidth =
    row?.reduce((sum, widget) => sum + widgetWidth(widget, totalColumns), 0) ?? totalColumns;
  const freeWidth = Math.max(0, totalColumns - usedWidth);
  const neighbourDelta = delta > 0 ? Math.max(0, delta - freeWidth) : delta;
  const resized = widgets.map((widget) => {
    if (widget.id === widgetId) return { ...widget, width };
    if (widget.id === bounds.neighbourId) {
      return { ...widget, width: neighbourWidth - neighbourDelta };
    }
    return widget;
  });

  return { widgets: resized, width, bounds };
}
