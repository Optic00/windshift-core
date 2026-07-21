/**
 * Select the CLDR plural category for a locale and count.
 * @param {string} localeCode
 * @param {number} count
 * @returns {Intl.LDMLPluralRule}
 */
export function getPluralCategory(localeCode, count) {
  return new Intl.PluralRules(localeCode).select(Number(count));
}

/**
 * Resolve a browser locale against the supported application locales.
 * Matching is case-insensitive, accepts underscore separators, and prefers
 * a region-specific match before falling back to the base language.
 * @param {string|undefined|null} browserLocale
 * @param {{ code: string }[]} supportedLocales
 * @param {string} defaultLocale
 * @returns {string}
 */
export function negotiateLocale(browserLocale, supportedLocales, defaultLocale) {
  if (!browserLocale) return defaultLocale;

  const normalized = browserLocale.replaceAll('_', '-').toLowerCase();
  const exact = supportedLocales.find((locale) => locale.code.toLowerCase() === normalized);
  if (exact) return exact.code;

  const language = normalized.split('-')[0];
  return (
    supportedLocales.find((locale) => locale.code.toLowerCase().split('-')[0] === language)?.code ??
    defaultLocale
  );
}
