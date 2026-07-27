const DRAFT_VERSION = 1;
const KEY_PREFIX = 'windshift:public-form-draft:';

export function publicFormDraftKey(slug, formId) {
  return `${KEY_PREFIX}${encodeURIComponent(slug)}:${formId}`;
}

export function hasDraftContent(draft) {
  if (draft?.title?.trim() || draft?.description?.trim()) return true;
  return Object.values(draft?.custom_fields || {}).some(
    (value) =>
      value === true || (value !== false && value !== null && value !== undefined && value !== '')
  );
}

export function loadPublicFormDraft(storage, slug, formId) {
  if (!storage || !slug || !formId) return null;
  try {
    const draft = JSON.parse(storage.getItem(publicFormDraftKey(slug, formId)) || 'null');
    if (!draft || draft.version !== DRAFT_VERSION) return null;
    return draft;
  } catch {
    return null;
  }
}

export function savePublicFormDraft(storage, slug, formId, draft) {
  if (!storage || !slug || !formId) return null;
  if (!hasDraftContent(draft)) {
    clearPublicFormDraft(storage, slug, formId);
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
  storage.setItem(publicFormDraftKey(slug, formId), JSON.stringify(stored));
  return stored;
}

export function clearPublicFormDraft(storage, slug, formId) {
  storage?.removeItem(publicFormDraftKey(slug, formId));
}
