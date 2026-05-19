import { bottomNavItems, mainNavItems } from '../../navigation/mainNavigation.js';
import { BUCKET } from '../buckets.js';
import { createCommand } from '../types.js';

/**
 * Global navigation commands derived from the same registry that drives the
 * sidebar. Excludes /dashboard (dead route — Phase 0 audit decision).
 * Adds Search and Workspaces entries which are not in the sidebar registry
 * but are surfaced as palette commands.
 */
export function globalNavigationProvider(ctx) {
  const { t, permissions } = ctx;
  const out = [];

  // Built-ins not in mainNavItems
  out.push(
    createCommand({
      id: 'nav-workspaces',
      label: t('commandPalette.commands.workspaces.label'),
      description: t('commandPalette.commands.workspaces.description'),
      bucket: BUCKET.GLOBAL_NAVIGATION,
      keywords: ['workspace', 'projects', 'organize'],
      url: '/workspaces',
    }),
    createCommand({
      id: 'nav-search',
      label: t('commandPalette.commands.search.label'),
      description: t('commandPalette.commands.search.description'),
      bucket: BUCKET.GLOBAL_NAVIGATION,
      keywords: ['search', 'find', 'items'],
      url: '/search',
    })
  );

  // Sidebar nav, gated by the same permissionStore keys
  for (const item of [...mainNavItems, ...bottomNavItems]) {
    if (item.permission && !permissions[item.permission]) continue;
    if (item.id === 'admin') continue; // admin tabs handled by adminProvider
    out.push(
      createCommand({
        id: `nav-${item.id}`,
        label: t(item.labelKey),
        description: '',
        bucket: BUCKET.GLOBAL_NAVIGATION,
        keywords: [item.id],
        url: item.href,
      })
    );
  }

  return out;
}
