/** Load comments whose agent-owner attribution is already permission-filtered server-side. */
export function loadAttributedComments(apiClient, itemId, params = {}) {
  return apiClient.getComments(itemId, params);
}

/** Load item history whose agent-owner attribution is already permission-filtered server-side. */
export function loadAttributedItemHistory(apiClient, itemId) {
  return apiClient.items.getHistory(itemId);
}

export function agentOwnerName(entry) {
  return typeof entry?.agent_owner_name === 'string' ? entry.agent_owner_name : '';
}
