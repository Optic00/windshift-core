/** Build a subject-id → permission-id Set map from compact assignment rows. */
export function buildPermissionAssignmentMap(rows, subjectKey) {
  const assignments = new Map();
  if (!Array.isArray(rows)) return assignments;

  for (const row of rows) {
    const subjectId = row?.[subjectKey];
    const permissionId = row?.permission_id;
    if (subjectId == null || permissionId == null) continue;
    if (!assignments.has(subjectId)) assignments.set(subjectId, new Set());
    assignments.get(subjectId).add(permissionId);
  }
  return assignments;
}

/**
 * Load the permission-manager request graph without per-user requests.
 * @param {any} apiClient
 * @param {{ permissions?: any[] }} [options]
 */
export async function loadPermissionManagerData(apiClient, options = {}) {
  const seededPermissions = options.permissions;
  const [permissions, users, groups, userGrants, groupGrants] = await Promise.all([
    Array.isArray(seededPermissions)
      ? Promise.resolve(seededPermissions)
      : apiClient.permissions.getAll(),
    apiClient.getUsers(),
    apiClient.groups.getAll(),
    apiClient.permissions.getAllUserGlobalPermissions(),
    apiClient.permissions.getAllGroupPermissions(),
  ]);

  return {
    permissions: Array.isArray(permissions) ? permissions : [],
    users: Array.isArray(users) ? users : [],
    groups: Array.isArray(groups) ? groups : [],
    userPermissions: buildPermissionAssignmentMap(userGrants, 'user_id'),
    groupPermissions: buildPermissionAssignmentMap(groupGrants, 'group_id'),
  };
}
