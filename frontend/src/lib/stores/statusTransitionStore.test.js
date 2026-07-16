import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api.js', () => ({
  api: {
    items: { getAvailableStatusTransitions: vi.fn() },
    workspaces: { getTransitionMatrix: vi.fn() },
  },
}));

const { api } = await import('../api.js');
const { statusTransitionStore } = await import('./statusTransitionStore.svelte.js');

function deferred() {
  let resolve;
  const promise = new Promise((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe('StatusTransitionStore workspace lifecycle', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    statusTransitionStore.reset();
  });

  it('does not let a previous workspace matrix overwrite the active workspace cache', async () => {
    const first = deferred();
    const second = deferred();
    api.workspaces.getTransitionMatrix.mockImplementation((workspaceId) =>
      workspaceId === 1 ? first.promise : second.promise
    );

    statusTransitionStore.initialize(1);
    const firstLoad = statusTransitionStore.preloadForWorkspace(1);

    statusTransitionStore.initialize(2);
    const secondLoad = statusTransitionStore.preloadForWorkspace(2);

    second.resolve({
      transitions: { '10:100': [{ id: 201, name: 'Workspace two' }] },
    });
    await secondLoad;

    first.resolve({
      transitions: { '10:100': [{ id: 101, name: 'Workspace one' }] },
    });
    await firstLoad;

    expect(statusTransitionStore.workspaceId).toBe(2);
    expect(statusTransitionStore.get(10, 100)).toEqual([{ id: 201, name: 'Workspace two' }]);
  });
});
