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
