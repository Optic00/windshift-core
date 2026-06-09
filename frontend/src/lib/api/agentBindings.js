// API client for the workspace-admin agent-binding surface (WI-88).
// Bindings tell the orchestrator which acting identity to use, which
// repo to mount, and which scopes the per-run ws token gets.

import { fetchAPI } from './core.js';

export const agentBindings = {
  /** List bindings configured in a workspace. */
  listForWorkspace: (workspaceId) => fetchAPI(`/workspaces/${workspaceId}/agent-bindings`),

  /**
   * Create a binding. The acting identity is validated server-side by
   * the WI-87 chokepoint; a 403 means the acting user isn't usable in
   * this workspace (owned by someone else, gated as a centralized
   * service user, etc.).
   *
   * @param {number} workspaceId
   * @param {{
   *   acting_user_id: number,
   *   repo_slug?: string,
   *   repo_remote_url?: string,
   *   repo_base_ref?: string,
   *   llm_connection_id?: number,
   *   scm_connection_id?: number,
   *   token_scopes?: string[],
   *   token_ttl_minutes?: number,
   *   max_runs_per_day?: number,
   * }} body
   */
  create: (workspaceId, body) =>
    fetchAPI(`/workspaces/${workspaceId}/agent-bindings`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  /** Delete a binding by id. */
  remove: (workspaceId, id) =>
    fetchAPI(`/workspaces/${workspaceId}/agent-bindings/${id}`, {
      method: 'DELETE',
    }),

  /**
   * Round-trip a prompt through the binding's LLM connection and return the
   * model's reply, plus — when the binding is repo-backed — a snapshot of the
   * cloned worktree's project root:
   *   { prompt, answer, repo?: { repo_slug, base_ref, entries: [{name, is_dir}], error? } }
   * A blank prompt uses a server default. 502 means the provider/connection
   * failed; 400 means the binding has no LLM connection. The repo block is
   * reported inline (its own `error`) so a working model reply still returns
   * even when the SCM/clone leg is broken; `repo` is absent for bindings with
   * no repo configured.
   */
  testLLM: (workspaceId, id, prompt) =>
    fetchAPI(`/workspaces/${workspaceId}/agent-bindings/${id}/test-llm`, {
      method: 'POST',
      body: JSON.stringify(prompt ? { prompt } : {}),
    }),

  /**
   * List the acting-identity options the workspace admin may pick:
   * owned agent users + allowlisted centralized service users (when the
   * WI-87 master flag is on). The chokepoint re-validates at create
   * time; this just keeps the picker honest.
   */
  getCandidates: (workspaceId) => fetchAPI(`/workspaces/${workspaceId}/agent-binding-candidates`),
};
