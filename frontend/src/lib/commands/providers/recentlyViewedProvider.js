import { BUCKET } from '../buckets.js';
import { createCommand } from '../types.js';

/** Add the top-ranked launcher for CommandPalette's intercepted recent-items
 * sub-palette; execute is a validation-only no-op. */
export function recentlyViewedProvider(ctx) {
  const { t } = ctx;
  return [
    createCommand({
      id: 'recently-viewed',
      label: t('commandPalette.recentlyViewed.label'),
      description: t('commandPalette.recentlyViewed.description'),
      bucket: BUCKET.RECENT,
      keywords: ['recent', 'recently', 'viewed', 'history', 'last'],
      submenu: 'recent',
      execute: () => {},
    }),
  ];
}
