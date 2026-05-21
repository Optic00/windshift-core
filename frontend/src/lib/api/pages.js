import { fetchAPI } from './core.js';
import { buildQueryString } from './utils.js';

/**
 * Workspace knowledge-pages API client. Mirrors logbook.js style: every
 * method returns a Promise from fetchAPI; auth is automatic via cookies
 * (core.js sets credentials: 'same-origin').
 */
export const pages = {
  /** Fetch the workspace page tree + flat list. */
  getTree: (workspaceId) => fetchAPI(`/workspaces/${workspaceId}/pages/tree`),

  /** Fetch a single page (404 on missing or no view permission). */
  getPage: (workspaceId, pageId) => fetchAPI(`/workspaces/${workspaceId}/pages/${pageId}`),

  /** Create a new page. parentId is optional (null/undefined = root). */
  createPage: (workspaceId, { title, content = '', parentId = null, isHome = false }) =>
    fetchAPI(`/workspaces/${workspaceId}/pages`, {
      method: 'POST',
      body: JSON.stringify({ title, content, parent_id: parentId, is_home: isHome }),
    }),

  /**
   * Update title/content on a page. Inheritance has its own admin-gated
   * endpoint (setInheritance below) — do not send the flag here; the
   * server rejects it as an unknown field shape and an editor without
   * admin would otherwise be able to flip inheritance via a normal save.
   */
  updatePage: (workspaceId, pageId, { title, content }) =>
    fetchAPI(`/workspaces/${workspaceId}/pages/${pageId}`, {
      method: 'PUT',
      body: JSON.stringify({ title, content }),
    }),

  /** Archive a page (and every descendant). */
  archivePage: (workspaceId, pageId) =>
    fetchAPI(`/workspaces/${workspaceId}/pages/${pageId}`, { method: 'DELETE' }),

  /** Reparent a page; pass null to move it to the workspace root. */
  movePage: (workspaceId, pageId, parentId) =>
    fetchAPI(`/workspaces/${workspaceId}/pages/${pageId}/move`, {
      method: 'POST',
      body: JSON.stringify({ parent_id: parentId }),
    }),

  /** Paginated revision history for a page. */
  getHistory: (workspaceId, pageId, { limit = 50, offset = 0 } = {}) =>
    fetchAPI(
      `/workspaces/${workspaceId}/pages/${pageId}/history${buildQueryString({ limit, offset })}`
    ),

  /** Fetch a single revision; must belong to the page. */
  getRevision: (workspaceId, pageId, revisionId) =>
    fetchAPI(`/workspaces/${workspaceId}/pages/${pageId}/history/${revisionId}`),

  /** Restore a revision; produces a new revision of type 'restore'. */
  restoreRevision: (workspaceId, pageId, revisionId) =>
    fetchAPI(`/workspaces/${workspaceId}/pages/${pageId}/history/${revisionId}/restore`, {
      method: 'POST',
    }),

  /** Read-only effective permissions + own ACL rows. */
  getPermissions: (workspaceId, pageId) =>
    fetchAPI(`/workspaces/${workspaceId}/pages/${pageId}/permissions`),

  /** Grant a new ACL row on a page. Requires page.admin on the target. */
  grantPermission: (workspaceId, pageId, { principalType, principalId, permissionLevel }) =>
    fetchAPI(`/workspaces/${workspaceId}/pages/${pageId}/permissions`, {
      method: 'POST',
      body: JSON.stringify({
        principal_type: principalType,
        principal_id: principalId,
        permission_level: permissionLevel,
      }),
    }),

  /** Revoke a single ACL row. The row must belong to the named page. */
  revokePermission: (workspaceId, pageId, permissionId) =>
    fetchAPI(`/workspaces/${workspaceId}/pages/${pageId}/permissions/${permissionId}`, {
      method: 'DELETE',
    }),

  /** Toggle the inherit_permissions flag on a page. */
  setInheritance: (workspaceId, pageId, inheritPermissions) =>
    fetchAPI(`/workspaces/${workspaceId}/pages/${pageId}/inheritance`, {
      method: 'PATCH',
      body: JSON.stringify({ inherit_permissions: inheritPermissions }),
    }),

  /** Unified knowledge search across pages (and future sources). */
  searchKnowledge: (workspaceId, query, { limit = 25 } = {}) =>
    fetchAPI(`/workspaces/${workspaceId}/knowledge/search${buildQueryString({ q: query, limit })}`),
};
