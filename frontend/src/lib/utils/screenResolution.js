/**
 * Resolves a mode-specific screen: item-type override, item-type create,
 * config-set mode, then config-set create. Returns null when unconfigured.
 *
 * @param {object|null|undefined} configSet
 * @param {number|null|undefined} itemTypeId
 * @param {'create'|'edit'|'view'} mode
 * @returns {number|null}
 */
export function resolveScreenId(configSet, itemTypeId, mode) {
  if (!configSet) return null;
  const modeKey = `${mode}_screen_id`;

  if (itemTypeId != null) {
    const itc = configSet.item_type_configs?.find((c) => c.item_type_id === itemTypeId);
    if (itc) {
      if (itc[modeKey]) return itc[modeKey];
      if (mode !== 'create' && itc.create_screen_id) return itc.create_screen_id;
    }
  }

  if (configSet[modeKey]) return configSet[modeKey];
  if (mode !== 'create' && configSet.create_screen_id) return configSet.create_screen_id;

  return null;
}
