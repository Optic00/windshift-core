import { api } from '../api.js';
import {
  RIGHTMOST_COLUMN_LIMIT,
  rightmostCapStatusIds,
} from '../features/collections/boardColumns.js';
import {
  fetchCollectionBacklog,
  fetchCollectionItemChanges,
  fetchCollectionItems,
  fetchItemsById,
  getCollection,
} from '../features/collections/collectionService.js';
import { currentRoute, GLOBAL_COLLECTION_VIEWS } from '../router.js';
import { calcHasMore } from '../utils/paginationUtils.js';
import { workspaceDataStore } from './workspaceDataStore.svelte.js';

const COLLECTION_VIEWS = new Set([
  'workspace-board',
  'workspace-board-config',
  'workspace-backlog',
  'workspace-list',
  'workspace-tree',
  'workspace-map',
  'workspace-roadmap',
]);

const BOARD_VIEWS = new Set(['workspace-board', 'collection-board']);
const LIST_VIEWS = new Set(['workspace-list', 'collection-list']);

const DEFAULT_PAGE_SIZE = 100;
const LIST_INITIAL_PAGE_SIZE = 50;
const LARGE_COLLECTION_PAGE_SIZE = 250;

function initialItemsPageSize(view) {
  if (view === 'workspace-list' || view === 'collection-list') return LIST_INITIAL_PAGE_SIZE;
  if (
    view === 'workspace-tree' ||
    view === 'collection-tree' ||
    view === 'workspace-map' ||
    view === 'collection-map' ||
    view === 'workspace-roadmap' ||
    view === 'collection-roadmap'
  ) {
    return LARGE_COLLECTION_PAGE_SIZE;
  }
  return DEFAULT_PAGE_SIZE;
}

class CollectionStore {
  // Reactive state
  items = $state([]);
  backlogItems = $state([]);
  collectionName = $state('Default');
  publicSlug = $state(null);
  loading = $state(false);

  // Items pagination
  itemsPagination = $state(null);
  itemsHasMore = $state(false);
  itemsLoadingMore = $state(false);

  // Split-fetch state for board views whose rightmost column is capped
  // (show_rightmost_column_last_50): { statusIds, total } or null. When
  // set, `items` holds the paged non-rightmost set (tracked by
  // itemsPagination) merged with the latest RIGHTMOST_COLUMN_LIMIT
  // rightmost-column items, and `total` is the server-side count of the
  // rightmost column. Keeps completed items from eating the page budget
  // of columns that actually render in full.
  rightmostCap = $state(null);

  // Backlog pagination
  backlogPagination = $state(null);
  backlogHasMore = $state(false);
  backlogLoadingMore = $state(false);

  // Sub-filter QL (clears on navigation)
  subFilterQL = $state('');
  // Raw filter rows backing the QL — kept so the SubFilterBar UI can hydrate
  // its builder when remounted on a different view of the same collection.
  subFilterRows = $state([]);

  // Server-side sort state
  sortableFields = $state([]);
  #sortBy = null;
  #sortDirection = null;

  // Internal tracking
  #wsId = null;
  #colId = null;
  #loadId = 0;
  #changesWatermark = null;
  #previousRouteKey = null;
  #currentView = null;
  #unsubscribe = null;

  constructor() {
    this.#unsubscribe = currentRoute.subscribe(($route) => {
      const view = $route.view;

      // Global collection views: /collections/:id/board etc.
      if (GLOBAL_COLLECTION_VIEWS.has(view)) {
        const colId = $route.params?.id || null;
        if (!colId) return;

        const routeKey = `${view}-global-${colId}`;
        if (routeKey === this.#previousRouteKey) return;
        this.#previousRouteKey = routeKey;

        this.load(null, colId, view);
        return;
      }

      // Workspace collection views
      const wsId = $route.params?.id;
      const colId = $route.params?.collectionId || null;

      if (!wsId || !COLLECTION_VIEWS.has(view)) {
        // Navigated away from a collection view — clear the route key
        // so that returning to the same collection triggers a fresh load.
        this.#previousRouteKey = null;
        return;
      }

      const routeKey = `${view}-${wsId}-${colId}`;
      if (routeKey === this.#previousRouteKey) return;
      this.#previousRouteKey = routeKey;

      this.load(wsId, colId, view);
    });
  }

  /**
   * Initial load: fetches page 1 of items and backlog, resets all pagination state.
   */
  async load(wsId, colId, view = this.#currentView) {
    const sameCollection = wsId === this.#wsId && colId === this.#colId;
    const viewChanged = view !== this.#currentView;
    const targetInitialLimit = initialItemsPageSize(view);

    // Switching between passive collection views does not need another network
    // roundtrip when the already-loaded item page is large enough and there is
    // no active server-side sort/filter. Board views may need capped-column
    // fetches, so they intentionally keep loading.
    if (
      sameCollection &&
      viewChanged &&
      this.items.length > 0 &&
      (this.itemsPagination?.limit ?? 0) >= targetInitialLimit &&
      !this.subFilterQL &&
      !this.#sortBy &&
      !this.#sortDirection &&
      !BOARD_VIEWS.has(view)
    ) {
      this.#currentView = view;
      return;
    }

    // Clear sub-filter and sort on navigation (workspace or collection change)
    if (wsId !== this.#wsId || colId !== this.#colId) {
      this.subFilterQL = '';
      this.subFilterRows = [];
      this.publicSlug = null;
      this.#sortBy = null;
      this.#sortDirection = null;
      this.sortableFields = [];
      this.#changesWatermark = null;
    }
    this.#wsId = wsId;
    this.#colId = colId;
    this.#currentView = view;
    const loadId = ++this.#loadId;

    this.loading = true;

    try {
      const [capStatusIds, , collection] = await Promise.all([
        this.#resolveBoardCap(wsId, colId, view),
        this.#primeChangesWatermark(),
        colId ? getCollection(colId) : Promise.resolve(null),
      ]);
      if (loadId !== this.#loadId) return; // stale

      const itemsLimit = targetInitialLimit;
      const [itemsResult, backlogResult, capResult] = await Promise.all([
        fetchCollectionItems(wsId, colId, {
          page: 1,
          limit: itemsLimit,
          sub_ql: this.subFilterQL || undefined,
          collection,
          ...this.#itemSortOptions(),
          ...this.#capExclusionFilter(capStatusIds),
        }),
        fetchCollectionBacklog(wsId, colId, {
          page: 1,
          limit: DEFAULT_PAGE_SIZE,
          sub_ql: this.subFilterQL || undefined,
          collection,
        }),
        this.#fetchCapItems(wsId, colId, capStatusIds, collection),
      ]);

      if (loadId !== this.#loadId) return; // stale

      this.items = capResult ? [...itemsResult.items, ...capResult.items] : itemsResult.items;
      this.rightmostCap = capResult
        ? { statusIds: capStatusIds, total: capResult.pagination?.total ?? capResult.items.length }
        : null;
      this.collectionName = itemsResult.collectionName;
      this.publicSlug = itemsResult.publicSlug ?? null;
      this.itemsPagination = itemsResult.pagination;
      this.itemsHasMore = calcHasMore(itemsResult.pagination);
      if (itemsResult.sortableFields?.length) {
        this.sortableFields = itemsResult.sortableFields;
      }

      this.backlogItems = backlogResult.items;
      this.backlogPagination = backlogResult.pagination;
      this.backlogHasMore = calcHasMore(backlogResult.pagination);
    } catch (error) {
      if (loadId !== this.#loadId) return;
      console.error('[collectionStore] Load failed:', error);
    } finally {
      if (loadId === this.#loadId) {
        this.loading = false;
      }
    }
  }

  /**
   * Resolves the status ids of the board's capped rightmost column for
   * split fetching. Returns null for non-board views, boards without the
   * show_rightmost_column_last_50 flag, or when resolution fails — the
   * view then falls back to plain paged loading.
   */
  async #resolveBoardCap(wsId, colId, view) {
    if (!BOARD_VIEWS.has(view)) return null;
    try {
      const config = await api.collections.getBoardConfiguration(colId || null, wsId || null);
      if (!config?.show_rightmost_column_last_50) return null;
      let statuses = [];
      if (!(config.columns?.length > 0)) {
        // Status-fallback columns: the rightmost column is derived from
        // the status list, so make sure it's loaded.
        if (wsId) {
          await workspaceDataStore.initialize(wsId);
        } else {
          await workspaceDataStore.initializeGlobal();
        }
        statuses = workspaceDataStore.statuses;
      }
      return rightmostCapStatusIds(config, statuses);
    } catch (error) {
      if (error?.status !== 404) {
        console.error('[collectionStore] board configuration lookup failed:', error);
      }
      return null;
    }
  }

  /** Query-param fragment excluding capped-column statuses from a paged items fetch. */
  #capExclusionFilter(capStatusIds = this.rightmostCap?.statusIds) {
    return capStatusIds?.length ? { status_id_not: capStatusIds.join(',') } : {};
  }

  /** Fetches the latest RIGHTMOST_COLUMN_LIMIT items of the capped column (null when no cap). */
  #fetchCapItems(wsId, colId, capStatusIds, collection) {
    if (!capStatusIds?.length) return Promise.resolve(null);
    return fetchCollectionItems(wsId, colId, {
      page: 1,
      limit: RIGHTMOST_COLUMN_LIMIT,
      sub_ql: this.subFilterQL || undefined,
      collection,
      status_id: capStatusIds.join(','),
      // Recency for the capped rightmost column. Mirrors itemRecencyValue on the
      // board, which prefers last_active_at (powers Bubble Mode) and falls back
      // to updated_at server-side via COALESCE-style ordering.
      order_by: 'last_active_at',
      sort_direction: 'desc',
    });
  }

  /**
   * Number of loaded items belonging to the paged (non-capped) set —
   * the count itemsPagination.total refers to. Equals items.length when
   * no rightmost cap is active.
   */
  get mainItemsLoadedCount() {
    if (!this.rightmostCap) return this.items.length;
    const capSet = new Set(this.rightmostCap.statusIds);
    return this.items.filter((item) => !capSet.has(item.status_id)).length;
  }

  /**
   * Append mode: fetch next items page and append to existing items.
   */
  async loadMoreItems() {
    if (!this.itemsHasMore || this.itemsLoadingMore) return;

    const nextPage = (this.itemsPagination?.page ?? 0) + 1;
    this.itemsLoadingMore = true;

    try {
      const result = await fetchCollectionItems(this.#wsId, this.#colId, {
        page: nextPage,
        limit: this.itemsPagination?.limit ?? DEFAULT_PAGE_SIZE,
        sub_ql: this.subFilterQL || undefined,
        ...this.#itemSortOptions(),
        ...this.#capExclusionFilter(),
      });

      this.items = [...this.items, ...result.items];
      this.itemsPagination = result.pagination;
      this.itemsHasMore = result.pagination
        ? result.pagination.page < result.pagination.total_pages
        : false;
    } catch (error) {
      console.error('[collectionStore] loadMoreItems failed:', error);
    } finally {
      this.itemsLoadingMore = false;
    }
  }

  /**
   * Append mode: fetch next backlog page and append to existing backlog items.
   */
  async loadMoreBacklog() {
    if (!this.backlogHasMore || this.backlogLoadingMore) return;

    const nextPage = (this.backlogPagination?.page ?? 0) + 1;
    this.backlogLoadingMore = true;

    try {
      const result = await fetchCollectionBacklog(this.#wsId, this.#colId, {
        page: nextPage,
        limit: this.backlogPagination?.limit ?? DEFAULT_PAGE_SIZE,
        sub_ql: this.subFilterQL || undefined,
      });

      this.backlogItems = [...this.backlogItems, ...result.items];
      this.backlogPagination = result.pagination;
      this.backlogHasMore = result.pagination
        ? result.pagination.page < result.pagination.total_pages
        : false;
    } catch (error) {
      console.error('[collectionStore] loadMoreBacklog failed:', error);
    } finally {
      this.backlogLoadingMore = false;
    }
  }

  /**
   * Replace mode: fetch a specific page of items (replaces current items).
   * Used by List view for page-based navigation and by Tree/Map for large fetches.
   */
  async setItemsPage(page, limit = DEFAULT_PAGE_SIZE) {
    this.loading = true;
    const loadId = ++this.#loadId;

    try {
      const result = await fetchCollectionItems(this.#wsId, this.#colId, {
        page,
        limit,
        sub_ql: this.subFilterQL || undefined,
        ...this.#itemSortOptions(),
      });

      if (loadId !== this.#loadId) return;

      this.items = result.items;
      this.collectionName = result.collectionName;
      this.publicSlug = result.publicSlug ?? null;
      this.itemsPagination = result.pagination;
      this.itemsHasMore = result.pagination
        ? result.pagination.page < result.pagination.total_pages
        : false;
      if (result.sortableFields?.length) {
        this.sortableFields = result.sortableFields;
      }
    } catch (error) {
      if (loadId !== this.#loadId) return;
      console.error('[collectionStore] setItemsPage failed:', error);
    } finally {
      if (loadId === this.#loadId) {
        this.loading = false;
      }
    }
  }

  /**
   * Refresh current data without resetting pagination.
   * Re-fetches page 1 with limit = current item count, preserving accumulated items.
   * Used by pollers and background updates.
   */
  async refresh() {
    if (!this.#wsId && !this.#colId) return;
    const loadId = ++this.#loadId;

    const itemsLimit = Math.max(initialItemsPageSize(this.#currentView), this.mainItemsLoadedCount);
    const backlogLimit = Math.max(DEFAULT_PAGE_SIZE, this.backlogItems.length);

    try {
      const [capStatusIds, , collection] = await Promise.all([
        this.#resolveBoardCap(this.#wsId, this.#colId, this.#currentView),
        this.#primeChangesWatermark(),
        this.#colId ? getCollection(this.#colId) : Promise.resolve(null),
      ]);
      if (loadId !== this.#loadId) return;

      const [itemsResult, backlogResult, capResult] = await Promise.all([
        fetchCollectionItems(this.#wsId, this.#colId, {
          page: 1,
          limit: itemsLimit,
          sub_ql: this.subFilterQL || undefined,
          collection,
          ...this.#itemSortOptions(),
          ...this.#capExclusionFilter(capStatusIds),
        }),
        fetchCollectionBacklog(this.#wsId, this.#colId, {
          page: 1,
          limit: backlogLimit,
          sub_ql: this.subFilterQL || undefined,
          collection,
        }),
        this.#fetchCapItems(this.#wsId, this.#colId, capStatusIds, collection),
      ]);
      if (loadId !== this.#loadId) return;

      this.items = capResult ? [...itemsResult.items, ...capResult.items] : itemsResult.items;
      this.rightmostCap = capResult
        ? { statusIds: capStatusIds, total: capResult.pagination?.total ?? capResult.items.length }
        : null;

      this.collectionName = itemsResult.collectionName;
      this.publicSlug = itemsResult.publicSlug ?? null;
      this.itemsPagination = itemsResult.pagination;
      this.itemsHasMore = calcHasMore(itemsResult.pagination);

      this.backlogItems = backlogResult.items;
      this.backlogPagination = backlogResult.pagination;
      this.backlogHasMore = calcHasMore(backlogResult.pagination);
    } catch (error) {
      if (loadId !== this.#loadId) return;
      console.error('[collectionStore] Refresh failed:', error);
    }
  }

  /**
   * Apply a sub-filter QL query and reload items.
   */
  setSubFilter(ql, rows = []) {
    this.subFilterQL = ql;
    this.subFilterRows = rows;
    if (this.#wsId || this.#colId) {
      this.load(this.#wsId, this.#colId, this.#currentView);
    }
  }

  /**
   * Clear the sub-filter and reload items.
   */
  clearSubFilter() {
    this.subFilterQL = '';
    this.subFilterRows = [];
    if (this.#wsId || this.#colId) {
      this.load(this.#wsId, this.#colId, this.#currentView);
    }
  }

  /**
   * Set server-side sort and reload from page 1.
   */
  setSorting(sortBy, sortDirection) {
    this.#sortBy = sortBy;
    this.#sortDirection = sortDirection;
    if (this.#wsId || this.#colId) {
      this.setItemsPage(1);
    }
  }

  /**
   * Re-trigger load() with current wsId/colId.
   */
  reload() {
    if (this.#wsId || this.#colId) {
      this.load(this.#wsId, this.#colId, this.#currentView);
    }
  }

  /**
   * Clear the route guard so the next navigation always triggers a fresh load.
   */
  invalidate() {
    this.#previousRouteKey = null;
  }

  async refreshItem(itemId) {
    try {
      const updated = await api.items.get(itemId);
      this.#applyUpdatedItem(updated);
    } catch (e) {
      console.error('[collectionStore] refreshItem failed:', e);
    }
  }

  /**
   * Poll for cheap deltas and patch loaded rows by ID. Falls back to a full
   * refresh when the delta implies structural uncertainty (new visible item,
   * server-side sort, or backlog membership changes).
   */
  async refreshDeltas() {
    if ((!this.#wsId && !this.#colId) || this.loading) return;
    if (this.#changesWatermark === null) {
      await this.#primeChangesWatermark();
      return;
    }

    try {
      const changes = await fetchCollectionItemChanges(this.#wsId, this.#colId, {
        since: this.#changesWatermark,
        sub_ql: this.subFilterQL || undefined,
      });
      this.#changesWatermark = changes?.watermark ?? this.#changesWatermark;

      if (changes?.requires_full_reload) {
        await this.refresh();
        return;
      }

      const removedIds = new Set(changes?.removed_item_ids ?? []);
      if (removedIds.size > 0) {
        this.#removeItemsById(removedIds);
      }

      const changedIds = [...new Set(changes?.changed_item_ids ?? [])].filter(
        (id) => !removedIds.has(id)
      );
      if (changedIds.length === 0) return;

      const loadedMainIds = new Set(this.items.map((item) => item.id));
      const loadedBacklogIds = new Set(this.backlogItems.map((item) => item.id));
      const loadedChangedIds = changedIds.filter(
        (id) => loadedMainIds.has(id) || loadedBacklogIds.has(id)
      );

      const hasNewVisibleItem = loadedChangedIds.length !== changedIds.length;
      const touchesBacklog = loadedChangedIds.some((id) => loadedBacklogIds.has(id));
      const usesServerOrderedItems =
        BOARD_VIEWS.has(this.#currentView) ||
        (LIST_VIEWS.has(this.#currentView) && (this.#sortBy || this.#sortDirection));
      if (hasNewVisibleItem || touchesBacklog || usesServerOrderedItems) {
        await this.refresh();
        return;
      }

      const updatedItems = await fetchItemsById(loadedChangedIds);
      for (const updated of updatedItems) {
        this.#applyUpdatedItem(updated);
      }
      // Nudge consumers that depend on array identity while preserving item
      // object identity for rows/cards that were patched in place.
      this.items = [...this.items];
      this.backlogItems = [...this.backlogItems];
    } catch (error) {
      console.error('[collectionStore] Delta refresh failed:', error);
    }
  }

  async #primeChangesWatermark() {
    const changes = await fetchCollectionItemChanges(this.#wsId, this.#colId, {
      sub_ql: this.subFilterQL || undefined,
    });
    this.#changesWatermark = changes?.watermark ?? 0;
  }

  #applyUpdatedItem(updated) {
    const idx = this.items.findIndex((i) => i.id === updated.id);
    if (idx !== -1) Object.assign(this.items[idx], updated);
    const bIdx = this.backlogItems.findIndex((i) => i.id === updated.id);
    if (bIdx !== -1) Object.assign(this.backlogItems[bIdx], updated);
  }

  #removeItemsById(ids) {
    const beforeBacklog = this.backlogItems.length;
    const capSet = new Set(this.rightmostCap?.statusIds ?? []);
    let removedItems = 0;
    let removedCapItems = 0;
    this.items = this.items.filter((item) => {
      if (!ids.has(item.id)) return true;
      if (capSet.has(item.status_id)) {
        removedCapItems++;
      } else {
        removedItems++;
      }
      return false;
    });
    this.backlogItems = this.backlogItems.filter((item) => !ids.has(item.id));

    const removedBacklog = beforeBacklog - this.backlogItems.length;
    if (removedItems > 0 && this.itemsPagination) {
      this.itemsPagination = {
        ...this.itemsPagination,
        total: Math.max(0, (this.itemsPagination.total ?? 0) - removedItems),
      };
      this.itemsHasMore = calcHasMore(this.itemsPagination);
    }
    if (removedCapItems > 0 && this.rightmostCap) {
      this.rightmostCap = {
        ...this.rightmostCap,
        total: Math.max(0, this.rightmostCap.total - removedCapItems),
      };
    }
    if (removedBacklog > 0 && this.backlogPagination) {
      this.backlogPagination = {
        ...this.backlogPagination,
        total: Math.max(0, (this.backlogPagination.total ?? 0) - removedBacklog),
      };
      this.backlogHasMore = calcHasMore(this.backlogPagination);
    }
  }

  #itemSortOptions() {
    if (BOARD_VIEWS.has(this.#currentView)) {
      return { order_by: 'frac_index', sort_direction: 'asc' };
    }

    if (LIST_VIEWS.has(this.#currentView)) {
      const opts = {};
      if (this.#sortBy) opts.order_by = this.#sortBy;
      if (this.#sortDirection) opts.sort_direction = this.#sortDirection;
      return opts;
    }

    return {};
  }

  destroy() {
    if (this.#unsubscribe) {
      this.#unsubscribe();
    }
  }
}

export const collectionStore = new CollectionStore();

/** Trigger a background refresh preserving current pagination */
export function reloadCollection() {
  collectionStore.refresh();
}

/** Poll for cheap collection deltas and patch loaded items when safe */
export function refreshCollectionDeltas() {
  return collectionStore.refreshDeltas();
}

/** Refresh a single item in the store without reloading the entire collection */
export function refreshCollectionItem(itemId) {
  return collectionStore.refreshItem(itemId);
}

/**
 * Backward-compatible derived-like store object.
 * Components using $collectionData will continue to work.
 */
export const collectionData = {
  subscribe(fn) {
    // Use $effect.root for reactive subscriptions to the class-based store
    let cleanup;
    const run = () => {
      const value = {
        items: collectionStore.items,
        backlogItems: collectionStore.backlogItems,
        collectionName: collectionStore.collectionName,
        loading: collectionStore.loading,
      };
      fn(value);
    };

    cleanup = $effect.root(() => {
      $effect(() => {
        run();
      });
    });

    return () => {
      if (cleanup) cleanup();
    };
  },
};
