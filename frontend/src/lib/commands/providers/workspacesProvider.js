import { BUCKET } from '../buckets.js';
import { createCommand } from '../types.js';

/**
 * "Go to <workspace>" quick navigation for up to 8 workspaces.
 *
 * Phase 0 audit fix: previous implementation showed all workspaces including
 * inactive ones. The sidebar dropdown hides inactive workspaces; we match
 * that behavior here so the palette doesn't surface deactivated workspaces.
 */
export function workspacesProvider(ctx) {
  const { workspaces, t } = ctx;
  const active = (workspaces || []).filter((ws) => ws.is_personal || ws.active !== false);

  return active.slice(0, 8).map((ws) => {
    const url = ws.is_personal ? '/personal' : `/workspaces/${ws.id}`;
    return createCommand({
      id: `goto-workspace-${ws.id}`,
      label: t('commandPalette.commands.goToWorkspace.label', { name: ws.name }),
      description: t('commandPalette.commands.goToWorkspace.description', { name: ws.name }),
      bucket: BUCKET.GLOBAL_NAVIGATION,
      keywords: ['goto', 'workspace', 'navigate', ws.name?.toLowerCase()].filter(Boolean),
      url,
    });
  });
}
