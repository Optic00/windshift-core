/** Load and normalize the complete read model shared by both run screens. */
export async function loadTestRunDetail(apiClient, workspaceId, runId) {
  const detail = await apiClient.tests.testRuns.getDetail(workspaceId, runId);
  if (!detail?.run) throw new Error('Test run not found');

  return {
    run: detail.run,
    testCases: Array.isArray(detail.test_cases)
      ? detail.test_cases.map((testCase) => ({
          ...testCase,
          test_steps: Array.isArray(testCase?.test_steps) ? testCase.test_steps : [],
        }))
      : [],
    results: Array.isArray(detail.results) ? detail.results : [],
    stepResults:
      detail.step_results && typeof detail.step_results === 'object' ? detail.step_results : {},
  };
}
