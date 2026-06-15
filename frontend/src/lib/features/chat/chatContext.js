/**
 * Build the narrow per-request context blob sent to the AI chat backend.
 *
 * Keep this intentionally small: it is used only to nudge the agent toward the
 * object the user is already viewing. Backend tools still re-check all
 * authorization before reading or mutating anything.
 */
export function buildChatContext(route) {
  if (!route) return undefined;

  if (route.view === 'workspace-actions') {
    const ctx = { view: route.view };
    const wsId = Number(route.params?.id);
    if (wsId) ctx.workspace_id = wsId;
    const actionId = Number(route.params?.actionId);
    if (actionId) ctx.action_id = actionId;
    return ctx;
  }

  if (route.view === 'workspace-pages') {
    const ctx = { view: route.view };
    const wsId = Number(route.params?.id);
    if (wsId) ctx.workspace_id = wsId;
    const pageId = Number(route.params?.pageId);
    if (pageId) ctx.page_id = pageId;
    return ctx;
  }

  if (route.view === 'item-detail') {
    const ctx = { view: route.view };
    const wsId = Number(route.params?.id);
    if (wsId) ctx.workspace_id = wsId;

    const itemId = Number(route.params?.itemId);
    if (itemId) {
      ctx.item_id = itemId;
    } else if (route.params?.itemKey) {
      ctx.item_key = route.params.itemKey;
    } else if (route.params?.workspaceKey && route.params?.itemNumber) {
      ctx.item_key = `${route.params.workspaceKey}-${route.params.itemNumber}`;
    }

    return Object.keys(ctx).length > 1 ? ctx : undefined;
  }

  return undefined;
}
