const defaultIndexCounts = {
  items: { current: 0, max: 20 },
  assets: { current: 0, max: 20 },
};

/** Load custom fields and every screen assignment with two bounded requests. */
export async function loadCustomFieldsOverview(apiClient) {
  const [fieldsOutcome, screensOutcome] = await Promise.allSettled([
    apiClient.customFields.getAll(),
    apiClient.screens.getAllWithFields(),
  ]);
  if (fieldsOutcome.status === 'rejected') {
    throw fieldsOutcome.reason;
  }
  const fieldsResult = fieldsOutcome.value;
  const screensResult = screensOutcome.status === 'fulfilled' ? screensOutcome.value : [];
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
