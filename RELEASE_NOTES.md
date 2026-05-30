# Windshift v0.7.2 — "Shiplaunch"

Windshift 0.7.2 introduces the coding-agent harness — Windshift can now run a coding agent against a work item on assignment — alongside CLI improvements that make Windshift a first-class backend for external task tools.

## Highlights

### Coding-agent harness

Windshift can spawn a per-assignment coding agent in an isolated git worktree with the `ws` CLI, `pi`, and the task extension baked in. The harness covers the full round trip: a walking-skeleton runner and worktree manager, per-run minted tokens, pi RPC plumbing and production runner wiring, workspace bindings with an assignee-change trigger, and SCM round-trip + pull-request creation against both GitHub and Gitea. An acting-identity security gate, a global-admin security surface, and a centralized-service-user allowlist (with a reason-input dialog on the Security page) govern which identities an agent may act as. A runs HTTP API and JS clients back the harness UI.

### Workspace-scoped priority listing for integrations

New `ws priority ls` command and `GET /rest/api/v1/workspaces/{id}/priorities` endpoint expose a workspace's priority catalog (id, name, sort order, default), scoped to the workspace's configuration set and mirroring `ws item-type ls` / `ws status ls`. This lets external task tools resolve priority names to ids for the right workspace — for example, the pi-tasks Windshift backend uses it to offer priority editing.

## Improvements

- LLM model refresh now covers all providers, with a new diagnostics widget.
- Board view supports swimlane grouping by item type.
- The request portal persists in-progress request drafts.

## Bug fixes

- SSO: allow configured private OIDC endpoints.
- Activity: use a window function for cleanup limits (Postgres-safe).
- Items: color linked-item status badges.

## Upgrade notes

- New agent-run and workspace-agent-binding tables are created automatically on startup.
- The coding-agent harness is inactive until a workspace has an agent binding configured; running agents also requires Docker on the runner host.
- `ws priority ls` and the new `/workspaces/{id}/priorities` endpoint require the v0.7.2 server; older clients are unaffected.
