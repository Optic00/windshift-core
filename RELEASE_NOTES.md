# Windshift v0.7.0 — "Shiplaunch"

---

> **Suitable for small-scale production use.**
>
> Windshift is maturing and can now be used for small-scale production workloads. Be aware that APIs, data formats, and configuration may still change between releases without guaranteed migration paths. We recommend keeping backups and testing upgrades in a staging environment before applying them.

---

A consolidation release. **Postgres** joins SQLite as a fully supported database. **Automation gets a credential vault** so action recipes stop being a place to paste tokens. The **AI agent can read and edit actions on your behalf**, with the editor reflecting changes live. **Portal customers** sign in with **passkeys**. The desktop app ships as a **signed macOS DMG**. Items belong to **multiple milestones**, configuration sets are **portable across instances**, and the **command palette** has been rebuilt.

## Features

### Action credentials

HTTP authentication can be stored once and referenced from any action capability, instead of being pasted into individual node configs. Credentials are encrypted at rest, scoped per-workspace, and managed at `/admin/action-credentials`. Sensitive headers (`Authorization`, `Cookie`, `X-Api-Key`) are rejected when configured inline so the credential picker is the only path. A bootstrap scanner flags pre-existing inline secrets so you can move them at your own pace, and all credential CRUD is audit-logged.

### Portal passkeys

Portal customers can register passkeys and sign in without an emailed magic link. A dismissable banner prompts enrolment after a successful magic-link sign-in.

### Items in multiple milestones

`items.milestone_id` becomes a join table. Backend, frontend, and the `ws` CLI all switch to `milestone_ids`; an idempotent backfill runs on first start. Take a backup before upgrading.

### Portable configuration sets

A configuration set exports as a self-contained JSON bundle and re-imports cleanly on another instance. Default sets are protected from being exported or overwritten.

### Hidden-title portal requests via templates

Request types carry a `title_template`. When the title field is hidden on the customer-facing form, the template renders server-side from the submitted virtual fields.

### Command palette rebuilt

Each surface (main nav, workspace views, admin) registers as a provider; results are grouped by bucket; the scorer is its own tested module. Workspace navigation now emits palette entries on every workspace page, not just the home view.

### Customer organisations with per-org ACLs

`member` and `manager` roles let a portal customer see their organisation's tickets without seeing the rest of the workspace.

### Item type changes

You can change an item's type after creation.

### `ws` CLI

New: `ws attachment list` / `download`, embedded comments in `ws task get`, and `ws diagram` CRUD. The command tree moved into `internal/wscli` so it is driven in-process from tests.

## Security hardening

- **Action containers always run with `--network none`.** Capabilities silently relying on host networking now fail closed.
- **Portal sessions are channel-bound.** A token for portal A cannot be replayed against portal B even with the same email. Hidden request types are gated server-side, not just in the UI.
- **AI guardrails.** Tool output fenced; feature policy applied before `connection_id` override; `max_steps` capped at 50.
- **SCIM group operations scoped to SCIM-managed rows.** A provisioner cannot reach groups it did not create.
- **SCM OAuth respects the workspace allowlist** on both `StartOAuth` and the connection listing.
- **Invitation tokens invalidated on reuse** against already-onboarded users.
- **Token cache invalidated on offboarding and deactivation** — deactivated users stop authenticating immediately, not on next cache eviction.
- **Supply chain.** Releases ship with signed npm packages, build provenance, GPG-signed checksums, and a `govulncheck` pass. Frontend dependencies are SHA-pinned behind a 24-hour cooldown gate.

## Reliability and operations

- **Schema migrations no longer log-and-continue.** A catalog-based runner halts on failure and names the affected migration.
- **Frac index is multi-node safe.** Atomic generation inside a transaction, retry on unique violations, and no process-local cache; a diagnostics panel surfaces fragmentation at a glance.
- **Notifications** went through a pass: watchers re-authorised at notification time, soft-delete with homepage-filter respect, tray-glance separated from acknowledgement, synchronous insert returning a real id, and a settings cache that refreshes on mutations.
- **Supervised long-running goroutines** — LDAP sync, logbook ingestion, SAML audit, rate-limit cleanup — are shutdown-clean and panic-safe.
- **`r.Context()` plumbed** through SSO, portal, forms, hub, webhook, channels, and middleware/permissions paths so client disconnects cancel DB lookups.
- **Jira import** auto-detects scoped vs legacy API tokens and surfaces the real upstream error instead of a flat 401.

## Bug fixes

Outcomes from a string of bughunt passes: portal-customer auth regression, item-move error surfacing, agent OAuth scopes, inactive workspaces leaking into selectors, profile labels accidentally minting shared labels, the system-theme listener silently no-oping, the markdown sanitiser dropping a closing `)`, attachment authorisation falling through for branding uploads, item-create silently using an archived default type, action-editor URL not reflecting the open action, collections builder losing state on hydrate, admin modals swallowing `/`, and the frontend `svelte-check` warning backlog cleared.

## Upgrade notes

- **Items move from `milestone_id` to `milestone_ids`.** An idempotent backfill drops the legacy column on first start. Take a backup. AI tool callers and the `ws` CLI must use `milestone_ids`.
- **Action credentials are encrypted at rest** with the existing SSO secret. Pre-existing inline credentials keep working; the scanner surfaces them in the admin so you can migrate at your own pace.
- **Action containers run with `--network none`.** Capabilities that previously relied on host networking need an explicit egress allowlist.
- **Default configuration sets cannot be exported or overwritten on import.**
- **Portal passkey tables** (`portal_webauthn_*`) are created on first start. No operator action required.
- **Jira API tokens.** Both scoped and legacy tokens are supported; the connection test auto-detects routing and surfaces the upstream reason on failure.

