import { api } from '../../api.js';

export function loadPublicFormBootstrap(slug) {
  return api.forms.getBootstrap(slug);
}

export function loadPublicFormDetail(slug, formId) {
  return api.forms.getFormDetail(slug, formId);
}
