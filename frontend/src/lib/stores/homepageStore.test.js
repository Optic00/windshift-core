import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  getLayout: vi.fn(),
  updateLayout: vi.fn(),
}));

vi.mock('../api.js', () => ({
  api: {
    homepage: mocks,
  },
}));

const { homepageStore } = await import('./homepageStore.svelte.js');

function homepageResponse(overrides = {}) {
  return {
    recent_workspaces: [],
    total_workspace_count: 2,
    total_item_count: 8,
    recently_viewed: [{ item_id: 10 }],
    recently_edited: [],
    recently_commented: [],
    watched_items: [],
    upcoming_milestones: [],
    layout: {
      sections: [
        { id: 'later', display_order: 1, widget_ids: [] },
        { id: 'first', display_order: 0, widget_ids: ['recent'] },
      ],
      widgets: [
        {
          id: 'recent',
          type: 'recent-workspaces',
          section_id: 'first',
          position: 0,
          width: 2,
        },
      ],
    },
    layout_revision: 'sha256:layout-v1',
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  homepageStore.reset();
});

describe('homepage route snapshot', () => {
  it('initializes dashboard data and layout with one aggregate request', async () => {
    mocks.get.mockResolvedValue(homepageResponse());

    await homepageStore.init('UTC');

    expect(mocks.get).toHaveBeenCalledOnce();
    expect(mocks.getLayout).not.toHaveBeenCalled();
    expect(homepageStore.totalWorkspaceCount).toBe(2);
    expect(homepageStore.recentlyViewed).toEqual([{ item_id: 10 }]);
    expect(homepageStore.sections.map((section) => section.id)).toEqual(['first', 'later']);
    expect(homepageStore.widgets).toHaveLength(1);
    expect(homepageStore.layoutRevision).toBe('sha256:layout-v1');
    expect(homepageStore.layoutLoaded).toBe(true);
  });

  it('shares the homepage request and snapshot between widget consumers', async () => {
    let resolveRequest;
    mocks.get.mockReturnValue(
      new Promise((resolve) => {
        resolveRequest = resolve;
      })
    );

    const first = homepageStore.getSnapshot();
    const second = homepageStore.getSnapshot();
    expect(mocks.get).toHaveBeenCalledOnce();

    const response = homepageResponse();
    resolveRequest(response);
    await expect(Promise.all([first, second])).resolves.toEqual([response, response]);

    await expect(homepageStore.getSnapshot()).resolves.toBe(response);
    expect(mocks.get).toHaveBeenCalledOnce();
  });

  it('replaces the shared snapshot on an explicit live refresh', async () => {
    mocks.get
      .mockResolvedValueOnce(homepageResponse({ total_item_count: 8 }))
      .mockResolvedValueOnce(homepageResponse({ total_item_count: 9 }));

    await homepageStore.getSnapshot();
    await homepageStore.refresh();

    expect(mocks.get).toHaveBeenCalledTimes(2);
    expect(homepageStore.totalItemCount).toBe(9);
    await expect(homepageStore.getSnapshot()).resolves.toMatchObject({ total_item_count: 9 });
  });

  it('refetches after the live snapshot is explicitly invalidated', async () => {
    mocks.get
      .mockResolvedValueOnce(homepageResponse({ total_item_count: 8 }))
      .mockResolvedValueOnce(homepageResponse({ total_item_count: 10 }));

    await homepageStore.getSnapshot();
    homepageStore.invalidateSnapshot();
    await homepageStore.getSnapshot();

    expect(mocks.get).toHaveBeenCalledTimes(2);
    expect(homepageStore.totalItemCount).toBe(10);
  });
});
