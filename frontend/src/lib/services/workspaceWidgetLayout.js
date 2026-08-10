import { clampWidgetWidth } from './widgetRegistry.js';

export function normalizeWorkspaceWidgets(widgets) {
  if (!Array.isArray(widgets)) return [];
  return widgets.map((widget) => ({
    ...widget,
    width: clampWidgetWidth(widget.type, widget.width),
  }));
}

export function captureWorkspaceWidgetWidths(widgets) {
  return new Map(widgets.map((widget) => [widget.id, widget.width]));
}

export function restoreRejectedWorkspaceWidgetWidths(currentWidgets, rejectedWidgets, savedWidths) {
  const rejectedWidths = captureWorkspaceWidgetWidths(rejectedWidgets);
  return currentWidgets.map((widget) => {
    if (widget.width !== rejectedWidths.get(widget.id) || !savedWidths.has(widget.id)) {
      return widget;
    }
    return { ...widget, width: savedWidths.get(widget.id) };
  });
}
