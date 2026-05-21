/**
 * English (en) - Knowledge pages (wiki) translations
 *
 * Surfaces:
 *   - PagesView (left tree + editor pane)
 *   - PageMoveDialog
 *   - PagePermissionsDialog
 */
export default {
  pages: {
    // Tree / left rail
    treeHeading: 'Pages',
    newPagePlaceholderChild: 'Child of {parent}',
    newPagePlaceholderRoot: 'New root page',
    newPageButton: 'New',
    treeLoading: 'Loading…',
    treeEmptyTitle: 'No pages yet',
    treeEmptyDescription: 'Type a title above and press New to create the first page.',

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
