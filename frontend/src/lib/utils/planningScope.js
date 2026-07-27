export function canChangePlanningScope(canManageGlobal, existingRecord) {
  return Boolean(canManageGlobal && !existingRecord);
}

export function preservePlanningScope(updates, existingRecord) {
  if (!existingRecord) return updates;

  const isGlobal = existingRecord.is_global !== false;
  return {
    ...updates,
    is_global: isGlobal,
    workspace_id: isGlobal ? null : existingRecord.workspace_id,
  };
}
