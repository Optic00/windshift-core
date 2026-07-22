import { fetchAPI } from './core.js';

export const forms = {
  getBootstrap: (slug) => fetchAPI(`/forms/${slug}/bootstrap`),
  getChannel: (slug) => fetchAPI(`/forms/${slug}`),
  getForms: (slug) => fetchAPI(`/forms/${slug}/forms`),
  getFormDetail: (slug, formId) => fetchAPI(`/forms/${slug}/forms/${formId}/detail`),
  getFormFields: (slug, formId) => fetchAPI(`/forms/${slug}/forms/${formId}/fields`),
  getCustomFields: (slug) => fetchAPI(`/forms/${slug}/custom-fields`),
  submit: (slug, data, attachments = []) => {
    if (attachments.length === 0) {
      return fetchAPI(`/forms/${slug}/submit`, {
        method: 'POST',
        body: JSON.stringify(data),
      });
    }
    const body = new FormData();
    body.set('submission', JSON.stringify(data));
    for (const attachment of attachments) body.append('attachments', attachment);
    return fetchAPI(`/forms/${slug}/submit`, { method: 'POST', body });
  },
};
