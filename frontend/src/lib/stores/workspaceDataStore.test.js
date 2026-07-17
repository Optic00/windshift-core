import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

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

  afterEach(() => {
    workspaceDataStore.reset();
    vi.useRealTimers();
    vi.restoreAllMocks();
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

  it('skips hidden-tab intervals and refreshes when the tab returns', async () => {
    vi.useFakeTimers();
    let visibilityState = 'visible';
    vi.spyOn(document, 'hidden', 'get').mockImplementation(() => visibilityState === 'hidden');
    vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibilityState);
    mocks.getWorkspace.mockResolvedValue({ id: 2, name: 'Workspace' });

    await workspaceDataStore.initialize(2);
    mocks.getWorkspace.mockClear();
    visibilityState = 'hidden';

    await vi.advanceTimersByTimeAsync(5 * 60 * 1000);
    expect(mocks.getWorkspace).not.toHaveBeenCalled();

    visibilityState = 'visible';
    document.dispatchEvent(new Event('visibilitychange'));
    expect(mocks.getWorkspace).toHaveBeenCalledTimes(1);
  });

  it('does not warn for expected background connectivity failures', async () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    mocks.getWorkspace.mockResolvedValueOnce({ id: 2, name: 'Workspace' });
    await workspaceDataStore.initialize(2);
    mocks.getWorkspace.mockRejectedValueOnce(
      Object.assign(new Error('offline'), { code: 'NETWORK_ERROR' })
    );

    await workspaceDataStore.refresh();

    expect(warnSpy).not.toHaveBeenCalled();
  });
});
