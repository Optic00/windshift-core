import { fetchAPI } from './core.js';
import { createCrudClient } from './createCrudClient.js';

// Integration providers (admin management)
export const integrationProviders = createCrudClient('/admin/integration-providers');

// User integration connections
export const userIntegrations = {
  getConnections: (options) => fetchAPI('/users/me/integration-connections', options),
  getAvailableProviders: (options) =>
    fetchAPI('/users/me/integration-connections/available', options),
  disconnect: (providerId) =>
    fetchAPI(`/users/me/integration-connections/${providerId}`, {
      method: 'DELETE',
    }),
  startOAuth: (slug) => fetchAPI(`/integrations/oauth/${slug}/start`),
};

// Todoist personal-task sync
export const todoistSync = {
  get: () => fetchAPI('/users/me/todoist-sync'),
  update: (data) =>
    fetchAPI('/users/me/todoist-sync', {
      method: 'PUT',
      body: JSON.stringify(data),
    }),
  getProjects: () => fetchAPI('/users/me/todoist-sync/projects'),
  run: () => fetchAPI('/users/me/todoist-sync/run', { method: 'POST' }),
};

// Item integration links
export const itemIntegrationLinks = {
  get: (itemId, options) => fetchAPI(`/items/${itemId}/integration-links`, options),
  create: (itemId, data) =>
    fetchAPI(`/items/${itemId}/integration-links`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  delete: (linkId) =>
    fetchAPI(`/item-integration-links/${linkId}`, {
      method: 'DELETE',
    }),
  refresh: (linkId) =>
    fetchAPI(`/item-integration-links/${linkId}/refresh`, {
      method: 'POST',
    }),
  search: (itemId, query, providerId) =>
    fetchAPI(
      `/items/${itemId}/integration-search?q=${encodeURIComponent(query)}&provider_id=${providerId}`
    ),
};
