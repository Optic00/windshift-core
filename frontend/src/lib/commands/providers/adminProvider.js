import { resolveAdminGroups } from '../../admin/adminNavigation.js';
import { BUCKET } from '../buckets.js';
import { createCommand } from '../types.js';

/**
 * Admin tab commands, gated by canAccessAdmin (not isSystemAdmin).
 *
 * Phase 0 audit fix: previous admin commands in CommandPalette.svelte were
 * gated by isSystemAdmin only. A user who has admin permission but isn't a
 * system-admin could see /admin in the sidebar but couldn't see admin
 * commands in the palette — inconsistent. Using canAccessAdmin matches the
 * sidebar behavior.
 */
export function adminProvider(ctx) {
  const { t, permissions } = ctx;
  if (!permissions?.canAccessAdmin) return [];

  const out = [];
  const groups = resolveAdminGroups(t);
  for (const group of groups) {
    for (const item of group.items) {
      out.push(
        createCommand({
          id: `admin-${item.id}`,
          label: item.label,
          description: item.description || '',
          bucket: BUCKET.ADMIN,
          keywords: ['admin', item.id.replace(/-/g, ' ')],
          url: `/admin/${item.id}`,
        })
      );
    }
  }
  return out;
}
