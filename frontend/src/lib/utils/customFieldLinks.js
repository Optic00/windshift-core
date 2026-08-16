import { safeHref } from './sanitize';

export function customFieldLinkHref(fieldType, value) {
  if (value === null || value === undefined || value === '') return null;

  const text = String(value).trim();
  if (fieldType !== 'url' && !/^https?:\/\//i.test(text)) return null;

  const href = safeHref(text);
  return href === '#' ? null : href;
}
