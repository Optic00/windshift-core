/** Load providers, connections and all per-connection auth summaries in two requests. */
export async function loadWorkspaceSCMOverview(apiClient, workspaceId) {
  const [availableProviders, connections] = await Promise.all([
    apiClient.workspaceSCM.getAvailableProviders(workspaceId),
    apiClient.workspaceSCM.getConnectionsOverview(workspaceId, { includeAuthStatus: true }),
  ]);
  const normalizedConnections = Array.isArray(connections) ? connections : [];
  return {
    availableProviders: Array.isArray(availableProviders) ? availableProviders : [],
    connections: normalizedConnections,
    authStatuses: Object.fromEntries(
      normalizedConnections
        .filter((connection) => connection?.auth_status)
        .map((connection) => [connection.id, connection.auth_status])
    ),
  };
}
