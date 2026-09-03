import { fetchV2Data } from '../core.js';
import { createCrudClient } from '../createCrudClient.js';

export const testSets = {
  ...createCrudClient('/test-sets', { parentPath: '/workspaces', v2: true, allV2: true }),
  getTestCases: (workspaceId, id) =>
    fetchV2Data(`/workspaces/${workspaceId}/test-sets/${id}/test-cases`),
  addTestCase: (workspaceId, setId, testCaseId) =>
    fetchV2Data(`/workspaces/${workspaceId}/test-sets/${setId}/test-cases`, {
      method: 'POST',
      body: JSON.stringify({ test_case_id: testCaseId }),
    }),
  removeTestCase: (workspaceId, setId, testCaseId) =>
    fetchV2Data(`/workspaces/${workspaceId}/test-sets/${setId}/test-cases/${testCaseId}`, {
      method: 'DELETE',
    }),
  getRuns: (workspaceId, setId) =>
    fetchV2Data(`/workspaces/${workspaceId}/test-sets/${setId}/runs`),
};
