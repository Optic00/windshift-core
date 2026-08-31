export function isCurrentZammadPanelContext(
  requestVersion,
  currentVersion,
  requestItemId,
  itemId,
  requestWorkspaceId,
  workspaceId
) {
  return (
    requestVersion === currentVersion &&
    requestItemId === itemId &&
    requestWorkspaceId === workspaceId
  );
}

export function isCurrentZammadMetadataRequest(
  requestVersion,
  currentVersion,
  requestConnectionId,
  selectedConnectionId,
  showCreate,
  dialogMode
) {
  return (
    requestVersion === currentVersion &&
    requestConnectionId === selectedConnectionId &&
    showCreate &&
    dialogMode === 'create'
  );
}

export function isCurrentZammadTimelineRequest(
  requestVersion,
  currentVersion,
  requestItemId,
  itemId,
  requestWorkspaceId,
  workspaceId
) {
  return isCurrentZammadPanelContext(
    requestVersion,
    currentVersion,
    requestItemId,
    itemId,
    requestWorkspaceId,
    workspaceId
  );
}
