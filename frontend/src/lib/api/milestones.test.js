import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('./core.js', () => ({
  fetchAPI: vi.fn(),
}));

vi.mock('./createCrudClient.js', () => ({
  createCrudClient: vi.fn(() => ({})),
}));

const { fetchAPI } = await import('./core.js');
const { milestones } = await import('./milestones.js');

describe('milestones API', () => {
  beforeEach(() => fetchAPI.mockReset());

  it('requests test statistics for unique milestone IDs in one call', async () => {
    fetchAPI.mockResolvedValue({});

    await milestones.getTestStatisticsMany([3, 4, 3]);

    expect(fetchAPI).toHaveBeenCalledOnce();
    expect(fetchAPI).toHaveBeenCalledWith('/milestones/test-statistics?ids=3,4');
  });
});
