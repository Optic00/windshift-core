/** Load status catalogs and every workflow transition with three requests. */
export async function loadStatusManagerData(apiClient) {
  const [statusCategories, statuses, workflows] = await Promise.all([
    apiClient.statusCategories.getAll(),
    apiClient.statuses.getAll(),
    apiClient.workflows.getAllWithTransitions(),
  ]);
  const normalizedCategories = Array.isArray(statusCategories) ? statusCategories : [];
  const normalizedWorkflows = Array.isArray(workflows) ? workflows : [];
  const workflowTransitions = normalizedWorkflows.flatMap((workflow) =>
    Array.isArray(workflow?.transitions) ? workflow.transitions : []
  );
  const counts = new Map();
  for (const transition of workflowTransitions) {
    if (transition?.from_status_id != null) {
      counts.set(transition.from_status_id, (counts.get(transition.from_status_id) ?? 0) + 1);
    }
    if (transition?.to_status_id != null && transition.to_status_id !== transition.from_status_id) {
      counts.set(transition.to_status_id, (counts.get(transition.to_status_id) ?? 0) + 1);
    }
  }

  return {
    statusCategories: normalizedCategories,
    workflowTransitions,
    statuses: Array.isArray(statuses)
      ? statuses.map((status) => ({
          ...status,
          transitionCount: counts.get(status.id) ?? 0,
        }))
      : [],
  };
}
