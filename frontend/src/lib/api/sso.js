import { safeRelativeRedirectPath } from '../utils/sanitize';
import { fetchAPI } from './core.js';

// SSO (Single Sign-On) endpoints
export const sso = {
  // Public endpoints (no auth required)
  getStatus: () => fetchAPI('/sso/status'),

  // Start SSO login (returns redirect URL)
  //
  // `redirectURI` is the relative path to return to after the IdP round-trip
  // (e.g. the current route). It is forwarded as `redirect_uri` so the backend
  // can replay it from the OIDC state row. The backend re-validates via
  // isValidRedirectURI, but we sanitize here too so we never send an absolute
  // or cross-origin value over the wire.
  startLogin: (slug, providerType = 'oidc', rememberMe = false, redirectURI = '') => {
    const params = new URLSearchParams();
    if (rememberMe) params.append('remember_me', 'true');
    if (redirectURI) {
      const sanitized = safeRelativeRedirectPath(redirectURI);
      if (sanitized) params.append('redirect_uri', sanitized);
    }
    const query = params.toString();
    // SAML and OIDC have different login URL patterns
    const basePath =
      providerType === 'saml' ? `/api/sso/${slug}/saml/login` : `/api/sso/login/${slug}`;
    return `${basePath}${query ? `?${query}` : ''}`;
  },

  // Admin endpoints (require system.admin)
  listProviders: () => fetchAPI('/sso/providers'),
  getProvider: (id) => fetchAPI(`/sso/providers/${id}`),
  createProvider: (data) =>
    fetchAPI('/sso/providers', {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  updateProvider: (id, data) =>
    fetchAPI(`/sso/providers/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  deleteProvider: (id) =>
    fetchAPI(`/sso/providers/${id}`, {
      method: 'DELETE',
    }),
  testProvider: (id) =>
    fetchAPI(`/sso/providers/${id}/test`, {
      method: 'POST',
    }),

  // User external accounts (require auth)
  getExternalAccounts: () => fetchAPI('/sso/external-accounts'),
  unlinkExternalAccount: (id) =>
    fetchAPI(`/sso/external-accounts/${id}`, {
      method: 'DELETE',
    }),
};
