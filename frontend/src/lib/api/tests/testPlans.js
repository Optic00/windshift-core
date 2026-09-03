import { fetchV2Data } from '../core.js';
import { createCrudClient } from '../createCrudClient.js';

// Test Plans (preferred terminology, same as testSets)
export const testPlans = {
  ...createCrudClient('/test-plans', { parentPath: '/workspaces', v2: true, allV2: true }),
  getTestCases: (workspaceId, id) =>
    fetchV2Data(`/workspaces/${workspaceId}/test-plans/${id}/test-cases`),
  addTestCase: (workspaceId, planId, testCaseId) =>
    fetchV2Data(`/workspaces/${workspaceId}/test-plans/${planId}/test-cases`, {
      method: 'POST',
      body: JSON.stringify({ test_case_id: testCaseId }),
    }),
  removeTestCase: (workspaceId, planId, testCaseId) =>
    fetchV2Data(`/workspaces/${workspaceId}/test-plans/${planId}/test-cases/${testCaseId}`, {
      method: 'DELETE',
    }),
  getRuns: (workspaceId, planId) =>
    fetchV2Data(`/workspaces/${workspaceId}/test-plans/${planId}/runs`),
};
