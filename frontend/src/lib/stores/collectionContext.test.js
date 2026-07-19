import { afterAll, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  routeSubscriber: null,
  fetchCollectionBacklog: vi.fn(),
  fetchCollectionItemChanges: vi.fn(),
  fetchCollectionItems: vi.fn(),
  getBoardConfiguration: vi.fn(),
}));

vi.mock('../api.js', () => ({
  api: {
    collections: {
      getBoardConfiguration: mocks.getBoardConfiguration,
    },
  },
}));

vi.mock('../features/collections/collectionService.js', () => ({
  fetchCollectionBacklog: mocks.fetchCollectionBacklog,
  fetchCollectionItemChanges: mocks.fetchCollectionItemChanges,
  fetchCollectionItems: mocks.fetchCollectionItems,
  fetchItemsById: vi.fn(),
  getCollection: vi.fn(),
}));

vi.mock('../router.js', () => ({
  currentRoute: {
    subscribe(callback) {
      mocks.routeSubscriber = callback;
      callback({ view: 'home', params: {} });
      return vi.fn();
    },
  },
  GLOBAL_COLLECTION_VIEWS: new Set(['collection-board', 'collection-list']),
}));

vi.mock('./workspaceDataStore.svelte.js', () => ({
  workspaceDataStore: {
    initialize: vi.fn(),
    initializeGlobal: vi.fn(),
    statuses: [],
  },
}));

const { collectionStore } = await import('./collectionContext.svelte.js');

function itemResult(options) {
  const page = options.page ?? 1;
  return {
    items: [{ id: page, status_id: 1 }],
    collectionName: 'Test board',
    pagination: { page, limit: options.limit, total: 2, total_pages: 2 },
    sortableFields: [],
    watermark: 1,
  };
}

describe('CollectionStore board ordering', () => {
  afterAll(() => collectionStore.destroy());

  it('applies Bubble Mode before pagination on loads, refreshes, and later pages', async () => {
    mocks.fetchCollectionItems.mockImplementation((_workspaceId, _collectionId, options) =>
      Promise.resolve(itemResult(options))
    );
    mocks.fetchCollectionBacklog.mockResolvedValue({
      items: [],
      pagination: { page: 1, limit: 100, total: 0, total_pages: 0 },
    });
    mocks.fetchCollectionItemChanges.mockResolvedValue({ watermark: 1 });
    mocks.getBoardConfiguration.mockResolvedValue(null);

    mocks.routeSubscriber({ view: 'workspace-board', params: { id: '42' } });
    await vi.waitFor(() => expect(mocks.fetchCollectionItems).toHaveBeenCalledTimes(1));
    expect(mocks.fetchCollectionItems).toHaveBeenLastCalledWith(
      '42',
      null,
      expect.objectContaining({
        page: 1,
        limit: 100,
        order_by: 'frac_index',
        sort_direction: 'asc',
      })
    );

    mocks.fetchCollectionItems.mockClear();
    collectionStore.setBoardSortMode('bubble');
    await vi.waitFor(() => expect(mocks.fetchCollectionItems).toHaveBeenCalledTimes(1));
    expect(mocks.fetchCollectionItems).toHaveBeenLastCalledWith(
      '42',
      null,
      expect.objectContaining({
        page: 1,
        order_by: 'last_active_at',
        sort_direction: 'desc',
      })
    );

    mocks.fetchCollectionItems.mockClear();
    await collectionStore.loadMoreItems();
    expect(mocks.fetchCollectionItems).toHaveBeenCalledWith(
      '42',
      null,
      expect.objectContaining({
        page: 2,
        order_by: 'last_active_at',
        sort_direction: 'desc',
      })
    );

    mocks.fetchCollectionItems.mockClear();
    await collectionStore.refresh();
    expect(mocks.fetchCollectionItems).toHaveBeenCalledWith(
      '42',
      null,
      expect.objectContaining({
        page: 1,
        order_by: 'last_active_at',
        sort_direction: 'desc',
      })
    );

    mocks.fetchCollectionItems.mockClear();
    mocks.routeSubscriber({ view: 'workspace-board', params: { id: '43' } });
    // A workspace change must synchronously hide the previous board while the
    // new request is in flight. MainApp reuses the mounted board component.
    expect(collectionStore.items).toEqual([]);
    expect(collectionStore.backlogItems).toEqual([]);
    expect(collectionStore.itemsPagination).toBeNull();
    await vi.waitFor(() => expect(mocks.fetchCollectionItems).toHaveBeenCalledTimes(1));
    expect(mocks.fetchCollectionItems).toHaveBeenLastCalledWith(
      '43',
      null,
      expect.objectContaining({
        page: 1,
        order_by: 'last_active_at',
        sort_direction: 'desc',
      })
    );

    mocks.fetchCollectionItems.mockClear();
    collectionStore.setBoardSortMode('rank');
    await vi.waitFor(() => expect(mocks.fetchCollectionItems).toHaveBeenCalledTimes(1));
    expect(mocks.fetchCollectionItems).toHaveBeenLastCalledWith(
      '43',
      null,
      expect.objectContaining({
        page: 1,
        order_by: 'frac_index',
        sort_direction: 'asc',
      })
    );
  });

  it('does not log expected connectivity failures from delta polling', async () => {
    mocks.fetchCollectionItems.mockImplementation((_workspaceId, _collectionId, options) =>
      Promise.resolve(itemResult(options))
    );
    mocks.fetchCollectionBacklog.mockResolvedValue({
      items: [],
      pagination: { page: 1, limit: 100, total: 0, total_pages: 0 },
    });
    mocks.getBoardConfiguration.mockResolvedValue(null);

    mocks.routeSubscriber({ view: 'workspace-list', params: { id: '44' } });
    await vi.waitFor(() => expect(collectionStore.loading).toBe(false));

    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    mocks.fetchCollectionItemChanges.mockRejectedValueOnce(
      Object.assign(new Error('offline'), { code: 'NETWORK_ERROR' })
    );

    await collectionStore.refreshDeltas();

    expect(errorSpy).not.toHaveBeenCalled();
  });

  it('loads only items for list views and only backlog for backlog views', async () => {
    mocks.fetchCollectionItems.mockClear();
    mocks.fetchCollectionBacklog.mockClear();
    mocks.fetchCollectionItemChanges.mockClear();
    mocks.fetchCollectionItems.mockImplementation((_workspaceId, _collectionId, options) =>
      Promise.resolve(itemResult(options))
    );
    mocks.fetchCollectionBacklog.mockResolvedValue({
      items: [{ id: 2, status_id: 1 }],
      collectionName: 'Backlog',
      pagination: { page: 1, limit: 100, total: 1, total_pages: 1 },
      watermark: 3,
    });

    mocks.routeSubscriber({ view: 'workspace-list', params: { id: '45' } });
    await vi.waitFor(() => expect(collectionStore.loading).toBe(false));

    expect(mocks.fetchCollectionItems).toHaveBeenCalledTimes(1);
    expect(mocks.fetchCollectionBacklog).not.toHaveBeenCalled();
    expect(mocks.fetchCollectionItemChanges).not.toHaveBeenCalled();

    mocks.fetchCollectionItems.mockClear();
    mocks.fetchCollectionBacklog.mockClear();
    mocks.routeSubscriber({ view: 'workspace-backlog', params: { id: '46' } });
    await vi.waitFor(() => expect(collectionStore.loading).toBe(false));

    expect(mocks.fetchCollectionItems).not.toHaveBeenCalled();
    expect(mocks.fetchCollectionBacklog).toHaveBeenCalledTimes(1);
  });

  it('polls from the oldest watermark returned by parallel board snapshots', async () => {
    mocks.fetchCollectionItems.mockImplementation((_workspaceId, _collectionId, options) =>
      Promise.resolve({ ...itemResult(options), watermark: 5 })
    );
    mocks.fetchCollectionBacklog.mockResolvedValue({
      items: [],
      pagination: { page: 1, limit: 100, total: 0, total_pages: 0 },
      watermark: 7,
    });
    mocks.fetchCollectionItemChanges.mockResolvedValue({
      watermark: 7,
      changed_item_ids: [],
      removed_item_ids: [],
    });
    mocks.getBoardConfiguration.mockResolvedValue(null);

    mocks.routeSubscriber({ view: 'workspace-board', params: { id: '47' } });
    await vi.waitFor(() => expect(collectionStore.loading).toBe(false));
    await collectionStore.refreshDeltas();

    expect(mocks.fetchCollectionItemChanges).toHaveBeenLastCalledWith(
      '47',
      null,
      expect.objectContaining({ since: 5 })
    );
  });

  it('single-flights board configuration for store and view consumers', async () => {
    mocks.getBoardConfiguration.mockClear();
    let resolveConfiguration;
    mocks.getBoardConfiguration.mockReturnValue(
      new Promise((resolve) => {
        resolveConfiguration = resolve;
      })
    );

    const storeLoad = collectionStore.getBoardConfiguration(48, null, { force: true });
    const viewLoad = collectionStore.getBoardConfiguration(48, null);

    expect(mocks.getBoardConfiguration).toHaveBeenCalledTimes(1);
    resolveConfiguration({ id: 9, columns: [] });
    await Promise.all([storeLoad, viewLoad]);
  });
});
