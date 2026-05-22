# Windshift v0.7.1 — "Knowledge Base"

Windshift 0.7.1 introduces workspace Pages: a built-in knowledge base for specs, runbooks, design notes, and long-lived project documentation. Pages are available in the web app, REST API, `ws` CLI, links, and AI agent tooling.

## Highlights

### Workspace Pages

Create Markdown pages inside each workspace, organize them into a tree, attach labels, and maintain immutable revision history. Pages are designed for team documentation that should live alongside work items instead of being scattered across external files.

### Page permissions and revision recovery

Pages support per-page ACLs, inheritance toggles, and admin-gated archive/restore flows. Revision history can be inspected and restored, including recovery of archived pages.

### CLI and API support

The `ws page` and `ws page-label` commands cover page creation, editing, moving, archiving, labels, history, restore, permissions, and inheritance. The v1 bearer-token API now exposes matching page, revision, ACL, and label endpoints for automation.

### AI agent page tools

Built-in agents can now search, read, create, update, move, archive, restore, and manage permissions for workspace Pages, subject to the same workspace roles and page ACLs as human users.

## Improvements

- Pages can be linked to work items using the Page link type.
- Page labels are included in CLI/API page responses and can be filtered from the CLI.
- Page history is paginated and available to automation clients.
- Page archive now records revision entries for affected subtree pages.
- Archived page content is removed from the search/chunk index until restored.
- Agent tokens minted by default include page scopes.

## Bug fixes

- Fixed the page history drawer showing an empty state despite existing revisions.
- Fixed restore so archived pages can be recovered by authorized admins.
- Fixed v1 page responses advertising a permissions URL that was not implemented.
- Fixed restore revision lookup to stay inside the restore transaction.
- Closed an archive permission-check race by authorizing the locked subtree inside the archive transaction.
- Fixed CLI structured JSON output dropping page `_links`.
- Fixed frontend Pages type-check warnings.

## Upgrade notes

- New page, revision, ACL, chunk, and label tables are created automatically on startup.
- Existing agent/CLI tokens may need to be re-minted if they predate page scopes and need access to Pages.
- Archive is still a soft-delete operation. Restoring a revision unarchives the addressed page; subtree recovery should be performed page-by-page where needed.

---

# Windshift v0.7.0 — "Shiplaunch"

> **Suitable for small-scale production use.**
>
> Windshift is maturing and can now support small teams in production. APIs, data formats, and configuration may still change between releases, so please keep backups and test upgrades before rolling them out.

Shiplaunch is a polish and platform release. It adds safer automation credentials, passkey login for portal customers, richer milestone planning, and a smoother AI-assisted workflow.

## Highlights

### Safer automation credentials

Automation recipes no longer need pasted tokens in each action. Store credentials once, choose them from a picker, and keep sensitive values out of action definitions.

### AI-assisted action editing

The AI agent can now inspect and update actions for you. The editor refreshes as changes happen, and several action-node save, delete, and execution issues have been fixed.

### Portal passkeys

Portal customers can register a passkey and sign in without waiting for an email link.

### Items can belong to multiple milestones

Items are no longer limited to one milestone. This makes roadmap, release, and sprint planning more flexible across the app and CLI.

### Portable configuration sets

Configuration sets can be exported and imported between Windshift instances, making setup and migration easier.

### Better command palette

The command palette has been rebuilt with grouped results and better workspace-aware navigation.

### More capable integrations

- Jira import now handles both scoped and legacy API tokens more clearly.
- SCM workflows can create milestone automation from tags.
- OpenRouter is available as an LLM provider with a refreshable model list.

## Recent improvements

- Image paste and upload now works from the create-item screen.
- AI chat refreshes open views as soon as an agent finishes work.
- Backlog filters and board column limits behave more reliably.
- Item history now shows clearer AI agent attribution.
- Admin user creation includes an Active toggle.
- Iteration tables have been tightened up visually, including non-wrapping type badges.

## Security and reliability

This release includes a broad hardening pass:

- Automation runs with stricter network isolation by default.
- Portal sessions are tied to the correct portal channel.
- Hidden portal request types and custom fields are enforced server-side.
- Attachment access is checked more consistently across item and branding uploads.
- Deactivated users stop authenticating immediately.
- Long-running background jobs shut down more cleanly and recover from panics.
- Failed migrations now stop startup instead of being logged and skipped.
- Releases include signed packages, checksums, provenance, and vulnerability checks.

## Bug fixes

This release also fixes issues around portal customer access, item moves, inactive workspace selectors, profile labels, system theme handling, markdown rendering, action-editor URLs, collection builder state, admin modal shortcuts, test folders, notification handling, and frontend type-check warnings.

## Upgrade notes

- **Back up before upgrading.** Items now use `milestone_ids` instead of a single `milestone_id`; the migration runs on first start.
- **Action credentials use the existing SSO secret for encryption.** Existing inline credentials still work, but the admin scanner will help you move them into the credential vault.
- **Action networking is stricter.** Capabilities that relied on unrestricted host networking may need explicit egress configuration.
- **Default configuration sets are protected.** They cannot be exported or overwritten during import.
- **Portal passkey tables are created automatically.** No manual setup is required.
