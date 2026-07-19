/** Load release-target SCM connections with one request for either scope. */
export async function loadMilestoneReleaseConnections(apiClient, workspaceId) {
  if (workspaceId) {
    const connections = (await apiClient.workspaceSCM.getConnections(workspaceId)) ?? [];
    return connections.map((connection) => ({
      ...connection,
      _workspaceName: null,
      _workspaceId: workspaceId,
    }));
  }

  const connections = (await apiClient.workspaceSCM.getAccessibleConnections()) ?? [];
  return connections.map((connection) => ({
    ...connection,
    _workspaceName: connection.workspace_name ?? null,
    _workspaceId: connection.workspace_id,
  }));
}
