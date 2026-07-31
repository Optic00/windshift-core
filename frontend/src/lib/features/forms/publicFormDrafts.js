const DRAFT_VERSION = 1;
const KEY_PREFIX = 'windshift:public-form-draft:';
const USER_SEGMENT = 'u';
// Bound retention window. Drafts older than this are purged on load so
// locally persisted form values — which can include personal or confidential
// information — cannot accumulate indefinitely on a shared or compromised
// browser.
const DRAFT_TTL_MS = 7 * 24 * 60 * 60 * 1000;

export function publicFormDraftKey(slug, formId, userId) {
  const base = `${KEY_PREFIX}${encodeURIComponent(slug)}:${formId}`;
  return userId ? `${base}:${USER_SEGMENT}:${userId}` : base;
}

export function hasDraftContent(draft) {
  if (draft?.title?.trim() || draft?.description?.trim()) return true;
  return Object.values(draft?.custom_fields || {}).some(
    (value) =>
      value === true || (value !== false && value !== null && value !== undefined && value !== '')
  );
}

function isDraftExpired(draft, now = Date.now()) {
  const updatedAt = Date.parse(draft?.updated_at || '');
  if (Number.isNaN(updatedAt)) return true;
  return now - updatedAt > DRAFT_TTL_MS;
}

function safeRemove(storage, key) {
  try {
    storage?.removeItem(key);
  } catch {
    // Storage can be unavailable in restricted browser contexts (private
    // mode, disabled cookies). Failing to delete a draft must not break form
    // editing.
  }
}

export function loadPublicFormDraft(storage, slug, formId, userId) {
  if (!storage || !slug || !formId) return null;
  try {
    const draft = JSON.parse(storage.getItem(publicFormDraftKey(slug, formId, userId)) || 'null');
    if (!draft || draft.version !== DRAFT_VERSION) return null;
    if (isDraftExpired(draft)) {
      safeRemove(storage, publicFormDraftKey(slug, formId, userId));
      return null;
    }
    return draft;
  } catch {
    return null;
  }
}

export function savePublicFormDraft(storage, slug, formId, draft, userId) {
  if (!storage || !slug || !formId) return null;
  if (!hasDraftContent(draft)) {
    clearPublicFormDraft(storage, slug, formId, userId);
    return null;
  }
  const stored = {
    version: DRAFT_VERSION,
    title: draft.title || '',
    description: draft.description || '',
    custom_fields: draft.custom_fields || {},
    current_step: draft.current_step || 1,
    updated_at: new Date().toISOString(),
  };
  try {
    storage.setItem(publicFormDraftKey(slug, formId, userId), JSON.stringify(stored));
    return stored;
  } catch {
    return null;
  }
}

export function clearPublicFormDraft(storage, slug, formId, userId) {
  safeRemove(storage, publicFormDraftKey(slug, formId, userId));
}
