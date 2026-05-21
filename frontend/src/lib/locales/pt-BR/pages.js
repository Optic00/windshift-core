/**
 * TODO(i18n): translate strings in this file from English to the locale.
 * Current content is the English source from en/pages.js so the keys
 * exist and users see English instead of literal key names.
 *
 * Surfaces:
 *   - PagesView (left tree + editor pane)
 *   - PageMoveDialog
 *   - PagePermissionsDialog
 */
export default {
  pages: {
    // Sidebar / drilldown nav
    backWorkspace: 'Workspace',
    treeHeading: 'Pages',
    addPageAria: 'Add page',
    untitled: 'Untitled',
    treeLoading: 'Loading…',
    treeEmptyTitle: 'No pages yet',
    treeEmptyDescription: 'Use the + button above to create your first page.',

    // Per-item kebab menu
    menuAddChild: 'Add child page',
    menuRename: 'Rename',
    menuMove: 'Move',
    menuPermissions: 'Permissions',
    menuArchive: 'Archive',

    // Page pane
    pageLoading: 'Loading page…',
    emptyPaneTitle: 'Knowledge Pages',
    emptyPaneDescription: 'Select a page from the tree, or create one to get started.',
    titlePlaceholder: 'Untitled',
    editorPlaceholder: 'Start writing…',
    tocHeading: 'On this page',
    tocAriaLabel: 'Table of contents',

    // Action buttons on the open page
    save: 'Save',
    move: 'Move',
    permissions: 'Permissions',
    archive: 'Archive',

    // Error fallbacks
    errorLoadTree: 'Failed to load pages',
    errorLoadPage: 'Failed to load page',
    errorSave: 'Failed to save',
    errorCreate: 'Failed to create page',
    errorArchive: 'Failed to archive',

    // Discard / archive confirms
    discardTitle: 'Discard unsaved changes?',
    discardMessage:
      'You have unsaved changes on the current page. They will be lost if you switch.',
    discardConfirm: 'Discard',
    discardCancel: 'Keep editing',
    archiveTitle: 'Archive "{title}"?',
    archiveMessage:
      'This archives the page and every child page. Phase 1 has no undo for this action.',
    archiveConfirm: 'Archive',

    // Move dialog
    moveTitle: 'Move "{title}"',
    moveSubtitle:
      'Pick a new parent. Pages under the current page are hidden because they would create a cycle.',
    moveSearchPlaceholder: 'Search pages…',
    moveRoot: 'Workspace root',
    moveButton: 'Move',
    moveCancel: 'Cancel',
    errorMove: 'Move failed',

    // Permissions dialog
    permsTitle: 'Page permissions',
    permsEffectiveAccess: 'Your effective access: {level}',
    permsEffectiveAccessNone: 'none',
    permsLoading: 'Loading…',
    permsInheritLabel: 'Inherit permissions from ancestors',
    permsInheritHint:
      'When inheritance is on and no explicit grants exist, workspace role permissions decide. Breaking inheritance with no grants restricts the page to admins.',
    permsExplicitGrants: 'Explicit grants',
    permsEmptyGrantsTitle: 'No explicit grants on this page.',
    permsEmptyGrantsDescription: 'Inheritance and workspace roles still apply.',
    permsColumnPrincipal: 'Principal',
    permsColumnLevel: 'Level',
    permsRemove: 'Remove',
    permsRemoveTitle: 'Remove permission?',
    permsRemoveMessage: 'This grant will be removed from the page. You can re-add it later.',
    permsRemoveConfirm: 'Remove',
    permsRemoveCancel: 'Cancel',
    permsClose: 'Close',
    permsAdd: 'Add',
    permsPrincipalUser: 'User',
    permsPrincipalGroup: 'Group',
    permsPrincipalRole: 'Role',
    permsLevelView: 'View',
    permsLevelEdit: 'Edit',
    permsLevelAdmin: 'Admin',
    permsPickUser: 'Pick a user',
    permsPickGroup: 'Pick a group',
    permsPickRole: 'Pick a role',
    permsErrorNoPrincipal: 'Pick a principal before adding the grant',
    permsErrorLoad: 'Failed to load permissions',
    permsErrorInherit: 'Failed to update inheritance',
    permsErrorGrant: 'Failed to add permission',
    permsErrorRevoke: 'Failed to revoke',
  },
};
