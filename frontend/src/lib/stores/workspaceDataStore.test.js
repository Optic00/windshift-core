import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  getWorkspace: vi.fn(),
}));

vi.mock('../api.js', () => ({
  api: {
    workspaces: {
      get: mocks.getWorkspace,
      getStatuses: vi.fn().mockResolvedValue([]),
      getProjects: vi.fn().mockResolvedValue([]),
    },
    itemTypes: { getAll: vi.fn().mockResolvedValue([]) },
    statusCategories: { getAll: vi.fn().mockResolvedValue([]) },
    getAssignableUsers: vi.fn().mockResolvedValue([]),
    milestones: { getAll: vi.fn().mockResolvedValue([]) },
    iterations: { getAll: vi.fn().mockResolvedValue([]) },
    priorities: { getAll: vi.fn().mockResolvedValue([]) },
    customFields: { getAll: vi.fn().mockResolvedValue({ data: [] }) },
  },
}));

const { workspaceDataStore } = await import('./workspaceDataStore.svelte.js');

describe('WorkspaceDataStore workspace switching', () => {
  beforeEach(() => {
    workspaceDataStore.reset();
    mocks.getWorkspace.mockReset();
  });

  it('synchronously clears reference data from the previous workspace', async () => {
    workspaceDataStore.workspaceId = 1;
    workspaceDataStore.workspace = { id: 1, name: 'Previous workspace' };
    workspaceDataStore.statuses = [{ id: 10, name: 'Previous status' }];
    workspaceDataStore.itemTypes = [{ id: 20, name: 'Previous type' }];
    workspaceDataStore.initialized = true;

    let resolveWorkspace;
    mocks.getWorkspace.mockReturnValue(
      new Promise((resolve) => {
        resolveWorkspace = resolve;
      })
    );

    const initialization = workspaceDataStore.initialize(2);

    expect(workspaceDataStore.workspaceId).toBe(2);
    expect(workspaceDataStore.workspace).toBeNull();
    expect(workspaceDataStore.statuses).toEqual([]);
    expect(workspaceDataStore.itemTypes).toEqual([]);
    expect(workspaceDataStore.initialLoading).toBe(true);

    resolveWorkspace({ id: 2, name: 'Current workspace' });
    await initialization;

    expect(workspaceDataStore.workspace).toEqual({ id: 2, name: 'Current workspace' });
    expect(workspaceDataStore.initialized).toBe(true);
  });
});
