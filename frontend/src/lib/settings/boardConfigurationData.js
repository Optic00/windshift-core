/**
 * Load the Board Configuration editor's route-specific data with one API
 * request while reusing the shell-owned workspace/global reference snapshot.
 */
export async function loadBoardConfigurationPageData(
  apiClient,
  referenceStore,
  workspaceId,
  collectionId
) {
  const referenceRequest = workspaceId
    ? referenceStore.initialize(workspaceId)
    : referenceStore.initializeGlobal();
  const [bootstrap] = await Promise.all([
    apiClient.collections.getBoardConfigurationBootstrap(collectionId, workspaceId),
    referenceRequest,
  ]);

  return {
    workspace: referenceStore.workspace ?? null,
    collection: bootstrap?.collection ?? null,
    boardConfiguration: bootstrap?.board_configuration ?? null,
    statuses: Array.isArray(bootstrap?.statuses)
      ? bootstrap.statuses
      : Array.isArray(referenceStore.statuses)
        ? referenceStore.statuses
        : [],
    customFieldDefinitions: Array.isArray(referenceStore.customFieldDefinitions)
      ? referenceStore.customFieldDefinitions
      : [],
  };
}
