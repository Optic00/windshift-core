/**
 * Deeply merges locale modules into a single translations object.
 * @param {Record<string, object>} modules - Named imports from locale-specific files
 * @returns {object} Merged translations
 */
export function createLocale(modules) {
  const result = {};

  for (const module of Object.values(modules)) {
    mergeInto(result, module);
  }

  return result;
}

export function mergeInto(target, source) {
  for (const [key, value] of Object.entries(source)) {
    if (key === '__proto__' || key === 'constructor' || key === 'prototype') {
      continue;
    }
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      const existing = Object.hasOwn(target, key) ? target[key] : undefined;
      const child = existing !== null && typeof existing === 'object' ? existing : {};
      target[key] = mergeInto(child, value);
    } else {
      target[key] = value;
    }
  }

  return target;
}
