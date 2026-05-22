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
