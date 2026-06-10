// API client for the workspace agent-skills library (WI-258). Skills are
// markdown knowledge packs attached to agent bindings; the run's prompt
// indexes them and the agent reads bodies on demand via `ws skill get`.

import { fetchAPI } from './core.js';

export const agentSkills = {
  /** List the workspace's skills (bodies included — the admin UI edits them). */
  listForWorkspace: (workspaceId) => fetchAPI(`/workspaces/${workspaceId}/agent-skills`),

  /**
   * Create a skill.
   * @param {number} workspaceId
   * @param {{ name: string, description?: string, body?: string, enabled?: boolean }} body
   */
  create: (workspaceId, body) =>
    fetchAPI(`/workspaces/${workspaceId}/agent-skills`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  /** Update a skill (full rewrite of name/description/body/enabled). */
  update: (workspaceId, id, body) =>
    fetchAPI(`/workspaces/${workspaceId}/agent-skills/${id}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    }),

  /** Delete a skill; binding attachments cascade away. */
  remove: (workspaceId, id) =>
    fetchAPI(`/workspaces/${workspaceId}/agent-skills/${id}`, {
      method: 'DELETE',
    }),
};
