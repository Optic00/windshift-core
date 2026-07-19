import { describe, expect, it, vi } from 'vitest';
import { loadBoardConfigurationPageData } from './boardConfigurationData.js';

describe('Board Configuration request graph', () => {
  it('uses one route bootstrap and reuses shell reference data', async () => {
    const apiClient = {
      collections: {
        getBoardConfigurationBootstrap: vi.fn().mockResolvedValue({
          collection: { id: 9, name: 'Delivery', is_public: true, public_slug: 'delivery' },
          board_configuration: { id: 12, columns: [] },
          statuses: [{ id: 30, name: 'Open' }],
        }),
        get: vi.fn(),
        getBoardConfiguration: vi.fn(),
      },
      items: { getAll: vi.fn() },
      workspaces: { get: vi.fn(), getStatuses: vi.fn() },
      statuses: { getAll: vi.fn() },
      customFields: { getAll: vi.fn() },
    };
    const referenceStore = {
      workspace: { id: 4, name: 'Workspace' },
      statuses: [{ id: 99 }],
      customFieldDefinitions: [{ id: 40, name: 'Impact' }],
      initialize: vi.fn().mockResolvedValue(undefined),
      initializeGlobal: vi.fn(),
    };

    const data = await loadBoardConfigurationPageData(apiClient, referenceStore, 4, 9);

    expect(apiClient.collections.getBoardConfigurationBootstrap).toHaveBeenCalledOnce();
    expect(apiClient.collections.getBoardConfigurationBootstrap).toHaveBeenCalledWith(9, 4);
    expect(referenceStore.initialize).toHaveBeenCalledWith(4);
    expect(referenceStore.initializeGlobal).not.toHaveBeenCalled();
    expect(apiClient.collections.get).not.toHaveBeenCalled();
    expect(apiClient.collections.getBoardConfiguration).not.toHaveBeenCalled();
    expect(apiClient.items.getAll).not.toHaveBeenCalled();
    expect(apiClient.workspaces.get).not.toHaveBeenCalled();
    expect(apiClient.workspaces.getStatuses).not.toHaveBeenCalled();
    expect(apiClient.statuses.getAll).not.toHaveBeenCalled();
    expect(apiClient.customFields.getAll).not.toHaveBeenCalled();
    expect(data).toEqual({
      workspace: { id: 4, name: 'Workspace' },
      collection: { id: 9, name: 'Delivery', is_public: true, public_slug: 'delivery' },
      boardConfiguration: { id: 12, columns: [] },
      statuses: [{ id: 30, name: 'Open' }],
      customFieldDefinitions: [{ id: 40, name: 'Impact' }],
    });
  });
});
