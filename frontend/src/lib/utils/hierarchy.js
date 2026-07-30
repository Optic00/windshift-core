export const GENERIC_SUBTASK_HIERARCHY_LEVEL = -1;

export function isGenericSubtaskType(itemType) {
  return Number(itemType?.hierarchy_level) === GENERIC_SUBTASK_HIERARCHY_LEVEL;
}

export function canItemTypeBeChildOf(childType, parentType) {
  if (!childType || !parentType || isGenericSubtaskType(parentType)) return false;
  if (isGenericSubtaskType(childType)) return true;
  return Number(childType.hierarchy_level) === Number(parentType.hierarchy_level) + 1;
}

export function childItemTypesForParent(itemTypes, parentType) {
  if (!parentType || isGenericSubtaskType(parentType)) return [];
  return (itemTypes || []).filter((type) => canItemTypeBeChildOf(type, parentType));
}

export function sortItemTypesByHierarchy(itemTypes) {
  return [...(itemTypes || [])].sort((a, b) => {
    const aGeneric = isGenericSubtaskType(a);
    const bGeneric = isGenericSubtaskType(b);
    if (aGeneric !== bGeneric) return aGeneric ? 1 : -1;
    return (
      Number(a.hierarchy_level) - Number(b.hierarchy_level) ||
      Number(a.sort_order ?? 0) - Number(b.sort_order ?? 0) ||
      String(a.name ?? '').localeCompare(String(b.name ?? ''))
    );
  });
}
