import { describe, expect, it, vi } from 'vitest';
import { loadCustomFieldsOverview } from './customFieldsData.js';

describe('custom fields screen request graph', () => {
  it('loads every screen assignment with two bounded requests', async () => {
    const apiClient = {
      customFields: {
        getAll: vi.fn().mockResolvedValue({
          data: [{ id: 7 }],
          index_counts: { items: { current: 2, max: 20 }, assets: { current: 1, max: 20 } },
        }),
      },
      screens: {
        getAllWithFields: vi.fn().mockResolvedValue([{ id: 1, fields: [{ id: 10 }] }, { id: 2 }]),
        getFields: vi.fn(),
      },
    };

    const loading = loadCustomFieldsOverview(apiClient);

    expect(apiClient.customFields.getAll).toHaveBeenCalledOnce();
    expect(apiClient.screens.getAllWithFields).toHaveBeenCalledOnce();
    expect(apiClient.screens.getFields).not.toHaveBeenCalled();
    const overview = await loading;
    expect(overview.customFields).toEqual([{ id: 7 }]);
    expect(overview.indexCounts.items.current).toBe(2);
    expect(overview.screens).toEqual([
      { id: 1, fields: [{ id: 10 }] },
      { id: 2, fields: [] },
    ]);
  });
});
