const defaultIndexCounts = {
  items: { current: 0, max: 20 },
  assets: { current: 0, max: 20 },
};

/** Load custom fields and every screen assignment with two bounded requests. */
export async function loadCustomFieldsOverview(apiClient) {
  const [fieldsResult, screensResult] = await Promise.all([
    apiClient.customFields.getAll(),
    apiClient.screens.getAllWithFields(),
  ]);
  return {
    customFields: Array.isArray(fieldsResult?.data)
      ? fieldsResult.data
      : Array.isArray(fieldsResult)
        ? fieldsResult
        : [],
    indexCounts: fieldsResult?.index_counts ?? defaultIndexCounts,
    screens: Array.isArray(screensResult)
      ? screensResult.map((screen) => ({
          ...screen,
          fields: Array.isArray(screen?.fields) ? screen.fields : [],
        }))
      : [],
  };
}
