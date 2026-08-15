import { authStore } from '../stores/auth.svelte.js';
import { createTemporalFormatter, getUserTimezone } from './dateFormatter.js';

/**
 * Format an instant in the acting user's validated display timezone.
 * @param {string|number|Date} value
 * @returns {string}
 */
export function formatAuthenticatedDateTime(value) {
  return formatAuthenticatedInstant(value, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

/**
 * Format an instant with custom options in the acting user's timezone.
 * @param {string|number|Date} value
 * @param {Intl.DateTimeFormatOptions} options
 * @returns {string}
 */
export function formatAuthenticatedInstant(value, options = {}) {
  const formatter = createTemporalFormatter(getUserTimezone(authStore?.currentUser));
  return formatter.formatInstant(value, options);
}
