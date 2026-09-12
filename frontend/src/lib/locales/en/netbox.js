export default { netbox: {
  tab: 'NetBox', connections: 'NetBox connections', connectionsDescription: 'Connect workspace items to selected NetBox devices and virtual machines.',
  addConnection: 'Add connection', editConnection: 'Edit connection', noConnections: 'No NetBox connections configured', loadFailed: 'Failed to load NetBox connections',
  name: 'Name', slug: 'Slug', baseUrl: 'HTTPS base URL', authScheme: 'Token version', bearer: 'API v2 bearer token', legacy: 'Legacy API token', apiToken: 'API token', enabled: 'Enabled',
  immutableHint: 'Base URL, slug and token version cannot be changed. Create a new connection to change them.', tokenPreserve: 'Leave blank to keep the stored token.',
  allWorkspaces: 'Allow all workspaces', allowedWorkspaces: 'Allowed workspaces', selectWorkspace: 'Select at least one workspace.',
  sharingNotice: 'Data available to this NetBox service account is visible to every Windshift item reader in the allowed workspaces. Use a least-privilege, read-only token.',
  save: 'Save connection', created: 'NetBox connection created', updated: 'NetBox connection updated', saveFailed: 'Failed to save NetBox connection',
  test: 'Test connection', testSucceeded: 'NetBox connection succeeded', testFailed: 'NetBox connection failed', noAllowedWorkspace: 'The connection has no allowed workspace. Select one before testing item access.',
  enableBeforeTesting: 'Enable connection before testing', editConflict: 'This connection changed while you were editing it. Discard your changes and reload before editing again.', discardAndReload: 'Discard changes and reload',
  delete: 'Delete connection', deleteConfirm: 'Delete “{name}”? This removes its local snapshots and links. Objects in NetBox are not changed.', deleted: 'NetBox connection deleted', deleteFailed: 'Failed to delete NetBox connection',
  panelTitle: 'NetBox', linkObject: 'Link object', noLinks: 'No NetBox object linked yet', noAvailableConnections: 'No NetBox connection is available for this workspace.', loadLinksFailed: 'Failed to load NetBox links',
  connection: 'Connection', type: 'Type', devices: 'Devices', virtualMachines: 'Virtual machines', search: 'Search', searchPlaceholder: 'Name or query', searchHint: 'Enter at least 2 characters.', noResults: 'No matching objects found.', previous: 'Previous', next: 'Next', link: 'Link', linkFailed: 'Failed to link NetBox object',
  open: 'Open in NetBox', refresh: 'Refresh', unlink: 'Unlink', refreshFailed: 'Refresh failed. The previous snapshot is unchanged.', unlinkFailed: 'Failed to unlink NetBox object', snapshotFrom: 'Snapshot from {date}', status: 'Status', site: 'Site', role: 'Role', ipv4: 'IPv4', ipv6: 'IPv6', close: 'Close'
} };
