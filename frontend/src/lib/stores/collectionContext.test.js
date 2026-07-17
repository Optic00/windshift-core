import { afterAll, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  routeSubscriber: null,
  fetchCollectionBacklog: vi.fn(),
  fetchCollectionItemChanges: vi.fn(),
  fetchCollectionItems: vi.fn(),
}));

vi.mock('../api.js', () => ({
  api: {
    collections: {
      getBoardConfiguration: vi.fn().mockResolvedValue(null),
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
});
