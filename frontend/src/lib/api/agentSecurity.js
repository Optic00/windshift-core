// API client for the global-admin coding-agent security surface
// (WI-87). The master flag + per-(user, workspace?) allowlist control
// whether workspace admins may bind agent runs to centralized service
// users — see Coding Agent Harness — Design §7 for why this is gated.

import { fetchAPI } from './core.js';

export const agentSecurity = {
  /** GET the master flag's current value. */
  getSettings: () => fetchAPI('/admin/agent-security/settings'),

  /**
   * Update the master flag. `reason` is required server-side so the
   * audit log entry has operator justification.
   * @param {{ allow_centralized_service_users: boolean, reason: string }} body
   */
  updateSettings: (body) =>
    fetchAPI('/admin/agent-security/settings', {
      method: 'PUT',
      body: JSON.stringify(body),
    }),

  /** GET the allowlist entries (a row per (user_id, workspace_id?)). */
  listAllowlist: () => fetchAPI('/admin/agent-security/allowlist'),

  /**
   * Add an allowlist entry. workspace_id may be omitted to grant the
   * acting user across every workspace.
   * @param {{ user_id: number, workspace_id?: number, reason: string }} body
   */
  addAllowlist: (body) =>
    fetchAPI('/admin/agent-security/allowlist', {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  /**
   * Remove an allowlist entry. `reason` is required.
   * @param {number} userId
   * @param {{ workspaceId?: number, reason: string }} opts
   */
  removeAllowlist: (userId, opts) => {
    const params = new URLSearchParams({ reason: opts.reason });
    if (opts.workspaceId) params.set('workspace_id', String(opts.workspaceId));
    return fetchAPI(`/admin/agent-security/allowlist/${userId}?${params}`, {
      method: 'DELETE',
    });
  },
};
