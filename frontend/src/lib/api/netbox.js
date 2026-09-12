import { fetchAPI } from './core.js';

const enc = encodeURIComponent;

export const netboxConnections = {
  getAll: (options) => fetchAPI('/admin/netbox-connections', options),
  get: (id, options) => fetchAPI(`/admin/netbox-connections/${enc(id)}`, options),
  create: (data) =>
    fetchAPI('/admin/netbox-connections', { method: 'POST', body: JSON.stringify(data) }),
  update: (id, data) =>
    fetchAPI(`/admin/netbox-connections/${enc(id)}`, { method: 'PUT', body: JSON.stringify(data) }),
  delete: (id) => fetchAPI(`/admin/netbox-connections/${enc(id)}`, { method: 'DELETE' }),
  test: (id) => fetchAPI(`/admin/netbox-connections/${enc(id)}/test`, { method: 'POST' }),
  forWorkspace: (workspaceId, options) =>
    fetchAPI(`/workspaces/${enc(workspaceId)}/netbox-connections`, options),
  search: (workspaceId, connectionId, params, options) => {
    const query = new URLSearchParams({
      object_type: params.object_type,
      q: params.q,
      limit: String(params.limit ?? 20),
      offset: String(params.offset ?? 0),
    });
    return fetchAPI(
      `/workspaces/${enc(workspaceId)}/netbox-connections/${enc(connectionId)}/search?${query}`,
      options
    );
  },
};

export const netboxLinks = {
  forItem: (itemId, options) => fetchAPI(`/items/${enc(itemId)}/netbox-links`, options),
  create: (itemId, data) =>
    fetchAPI(`/items/${enc(itemId)}/netbox-links`, { method: 'POST', body: JSON.stringify(data) }),
  delete: (itemId, linkId) =>
    fetchAPI(`/items/${enc(itemId)}/netbox-links/${enc(linkId)}`, { method: 'DELETE' }),
  refresh: (itemId, linkId) =>
    fetchAPI(`/items/${enc(itemId)}/netbox-links/${enc(linkId)}/refresh`, { method: 'POST' }),
};
