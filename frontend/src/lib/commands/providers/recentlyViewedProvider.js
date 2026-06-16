import { BUCKET } from '../buckets.js';
import { createCommand } from '../types.js';

/**
 * Surface a launcher entry that opens the "recently viewed work items"
 * sub-palette. It lives in the top RECENT bucket so that, with an empty
 * query, it is the first entry when the palette opens. Activating it does not
 * navigate — CommandPalette intercepts `submenu: 'recent'` and switches the
 * palette into its recent-items mode (the `execute` no-op only satisfies the
 * dev-time createCommand validation and is never called).
 */
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
