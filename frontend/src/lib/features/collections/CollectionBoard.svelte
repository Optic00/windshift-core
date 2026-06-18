<script>
  import { onMount, untrack } from 'svelte';
  import { useEventListener } from 'runed';
  import { t } from '../../stores/i18n.svelte.js';
  import { api } from '../../api.js';
  import { navigate } from '../../router.js';
  import { collectionStore, refreshCollectionDeltas, reloadCollection } from '../../stores/collectionContext.js';
  import { useGradientStyles, loadWorkspaceGradient } from '../../stores/workspaceGradient.svelte.js';
  import QuickAddForm from './QuickAddForm.svelte';
  import { getCollection, checkItemVisibility } from './collectionService.js';
  import { RIGHTMOST_COLUMN_LIMIT, buildDisplayColumns } from './boardColumns.js';
  import { infoToast, successToast, warningToast } from '../../stores/toasts.svelte.js';
  import { Plus, ChevronDown, ChevronRight, MoreHorizontal, Layers } from '@lucide/svelte';
  import ItemPicker from '../../pickers/ItemPicker.svelte';
  import { buildIterationPickerConfig } from '../iterations/iterationPickerUtils.js';
  import { itemTypeIconMap } from '../../utils/icons.js';
  import { draggable, dropTargetForElements } from '@atlaskit/pragmatic-drag-and-drop/element/adapter';
  import { attachClosestEdge, extractClosestEdge } from '@atlaskit/pragmatic-drag-and-drop-hitbox/closest-edge';
  import ItemDetail from '../items/ItemDetail.svelte';
  import DropIndicator from '../../layout/DropIndicator.svelte';
  import ViewHeader from '../../layout/ViewHeader.svelte';
  import StaticViewBackground from '../../layout/StaticViewBackground.svelte';
  import Button from '../../components/Button.svelte';
  import SearchInput from '../../components/SearchInput.svelte';
  import SubFilterBar from './SubFilterBar.svelte';
  import CardFieldChip from './CardFieldChip.svelte';
  import DependencySummary from './DependencySummary.svelte';
  import ItemKey from '../items/ItemKey.svelte';
  import CollectionViewSwitcher from './CollectionViewSwitcher.svelte';
  import Tooltip from '../../components/Tooltip.svelte';
  import DropdownMenu from '../../layout/DropdownMenu.svelte';
  import { backlogStore, workspaceDataStore, statusTransitionStore } from '../../stores/index.js';
  import { useWorkItemPoller } from '../../composables/useWorkItemPoller.svelte.js';
  import { agentRuns } from '../../stores/agentRuns.svelte.js';
  import { getVisibleColor, hexToRgb } from '../../utils/colorUtils.js';
  import { showCreatedItemToast } from '../../utils/createdItemToast.js';

  // Props
  let { workspaceId, collectionId = null } = $props();

  // Reference data from shared workspace store
  let workspace = $derived(workspaceDataStore.workspace);
  let itemTypes = $derived(workspaceDataStore.itemTypes);
  let statuses = $derived(workspaceDataStore.statuses);
  let users = $derived(workspaceDataStore.users);
  let priorities = $derived(workspaceDataStore.priorities);
  let milestones = $derived(workspaceDataStore.milestones);
  let iterations = $derived(workspaceDataStore.iterations);
  let wdsLabels = $derived(workspaceDataStore.labels);
  let projects = $derived(workspaceDataStore.projects);
  let customFieldDefinitions = $derived(workspaceDataStore.customFieldDefinitions);

  // Dynamic view-specific state — derived directly from central store
  let items = $derived(collectionStore.items);
  let transitions = $state([]);
  let boardConfig = $state(null);
  let cardFields = $derived((boardConfig?.card_fields || []).slice().sort((a, b) => a.display_order - b.display_order));

  let loading = $state(true);
  let currentCollectionName = $derived(collectionStore.collectionName);
  let setupTimeout;
  let setupElements = new Map(); // Track which elements have drag/drop set up and their cleanup functions
  let pendingDrops = new Set(); // Track pending drop operations to prevent duplicates
  let showItemModal = $state(false);
  let selectedItemId = $state(null);
  let searchQuery = $state('');

  // Dependency/blocker hover summary: lazily-fetched item links cached per
  // item so re-renders (drag, filtering) don't refetch. Keyed by item id →
  // merged outgoing+incoming link list.
  let dependencyLinksByItem = $state({});
  let dependencyLinksToken = 0; // guards against stale async when items change
  const DEPENDENCY_LINK_CHUNK = 200; // ids per batched /links/batch request (server cap 500)

  // Quick-add state per column
  let quickAddState = $state({});
  let workspaces = $state([]);

  // Backlog functionality
  let backlogItems = $derived(collectionStore.backlogItems);

  // Iteration filter state
  let allIterations = $state([]);
  let iterationFilterId = $state(null);

  // Swimlane grouping state
  let groupByItemTypeId = $state(null);
  let excludeRightmostSwimlaneParents = $state(false);
  let swimlaneCollapsed = $state({});

  // Edge-based drag state
  let dragState = $state(new Map()); // Track drag state for each item: { isDragging: boolean, closestEdge: 'top'|'bottom'|null }
  let boardAnnouncement = $state('');

  // Centralized gradient styling
  const styles = useGradientStyles();

  // Listen for newly created items
  async function handleRefreshWorkItems(event) {
    if (event.detail?.itemId) {
      try {
        const newItem = await api.items.get(event.detail.itemId);
        // When viewing a collection, accept items from any workspace (the collection defines scope).
        // Otherwise fall back to current-workspace check.
        const belongsToView = collectionId
          ? true
          : Number(newItem.workspace_id) === Number(workspaceId);
        if (belongsToView) {
          if (newItem.status_id) {
            // Item has a status, add it to the board (at the end, since board is ordered by rank)
            collectionStore.items = [...collectionStore.items, newItem];
          } else {
            // Item has no status, add it to backlog (at the end)
            collectionStore.backlogItems = [...collectionStore.backlogItems, newItem];
          }
          // Preload transitions for the new item before setting up drag and drop
          await statusTransitionStore.preloadForItems([newItem]);
          // Re-setup drag and drop for the new item
          setTimeout(() => {
            setupDragAndDrop();
          }, 100);
        }
      } catch (error) {
        console.error('Failed to load new item:', error);
      }
    }
  }

  useEventListener(() => window, 'refresh-work-items', handleRefreshWorkItems);

  // Quick-add functions
  function initQuickAdd(columnId, statusId, quickAddKey = columnId, parentId = null) {
    const availableTypes = (itemTypes || []).slice().sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0));
    if (availableTypes.length === 0) return;

    let preselectedWorkspaceId = workspaceId ? parseInt(workspaceId) : (workspaces.length === 1 ? workspaces[0].id : null);

    let preselectedItemTypeId = availableTypes[0]?.id ?? null;
    try {
      const savedId = parseInt(localStorage.getItem('board-quickadd-last-item-type-id') || '', 10);
      if (savedId && availableTypes.some(t => t.id === savedId)) {
        preselectedItemTypeId = savedId;
      }
    } catch (e) { /* ignore storage errors */ }

    quickAddState[quickAddKey] = {
      show: true,
      workspaceId: preselectedWorkspaceId,
      itemTypeId: preselectedItemTypeId,
      availableTypes,
      statusId,
      parentId,
      title: '',
      error: null
    };

    setTimeout(() => {
      const textarea = /** @type {HTMLTextAreaElement | null} */ (document.querySelector(`textarea[data-quick-add-parent="${quickAddKey}"]`));
      if (textarea) textarea.focus();
    }, 0);
  }

  function cancelQuickAdd(quickAddKey) {
    delete quickAddState[quickAddKey];
  }

  function updateQuickAddField(quickAddKey, field, value) {
    if (quickAddState[quickAddKey]) {
      quickAddState[quickAddKey][field] = value;
      quickAddState[quickAddKey].error = null;
    }
  }

  async function createColumnItem(quickAddKey) {
    const state = quickAddState[quickAddKey];
    if (!state) return;

    if (!state.workspaceId) {
      quickAddState[quickAddKey].error = 'Please select a workspace';
      return;
    }
    if (!state.itemTypeId) {
      quickAddState[quickAddKey].error = 'Please select an item type';
      return;
    }
    if (!state.title?.trim()) {
      quickAddState[quickAddKey].error = 'Please enter a title';
      return;
    }

    try {
      const payload = {
        workspace_id: state.workspaceId,
        item_type_id: state.itemTypeId,
        title: state.title.trim(),
        description: '',
        status_id: state.statusId
      };
      if (state.parentId) {
        payload.parent_id = state.parentId;
      }

      const newItem = await api.items.create(payload);

      if (collectionId) {
        const collection = await getCollection(collectionId);
        if (collection) {
          const filters = { collection_id: collectionId };
          const isVisible = await checkItemVisibility(newItem.id, filters);
          if (!isVisible) {
            const selectedWorkspace = workspaces.find(w => w.id === state.workspaceId);
            const workspaceName = selectedWorkspace?.name || 'another workspace';
            infoToast(`Card created in ${workspaceName} but won't appear here due to collection filters`, 'Card created successfully');
          }
        }
      }

      try {
        localStorage.setItem('board-quickadd-last-item-type-id', String(state.itemTypeId));
      } catch (e) { /* ignore storage errors */ }

      // Optimistic local add: fetch full item and add to store directly
      const fullItem = await api.items.get(newItem.id);
      collectionStore.items = [...collectionStore.items, fullItem];
      await statusTransitionStore.preloadForItems([fullItem]);
      setTimeout(() => setupDragAndDrop(), 100);

      // Toast feedback
      showCreatedItemToast(fullItem);
      if (collectionStore.itemsHasMore) {
        warningToast('The board has more items than can be displayed. Use "Load More" to see all items.');
      }

      cancelQuickAdd(quickAddKey);
    } catch (error) {
      console.error('Failed to create item:', error);
      quickAddState[quickAddKey].error = 'Failed to create item: ' + (error.message || error);
    }
  }

  onMount(async () => {
    if (workspaceId) {
      await loadWorkspaceGradient(workspaceId);
      await workspaceDataStore.initialize(workspaceId);
    } else {
      await workspaceDataStore.initializeGlobal();
    }
    try {
      workspaces = await api.workspaces.getAll() || [];
    } catch (error) {
      console.error('Failed to load workspaces:', error);
      workspaces = [];
    }
    // Load iterations for iteration filter (workspace only)
    if (workspaceId) {
      try {
        const iters = await api.iterations.getAll({ workspace_id: workspaceId, include_global: !workspace?.is_personal });
        allIterations = iters || [];
      } catch (error) {
        console.error('Failed to load iterations:', error);
      }
      // Restore persisted iteration filter
      const saved = localStorage.getItem(`board-iteration-filter-${workspaceId}`);
      if (saved) {
        const id = parseInt(saved);
        if (allIterations.some(i => i.id === id)) {
          iterationFilterId = id;
        } else {
          localStorage.removeItem(`board-iteration-filter-${workspaceId}`);
        }
      }
    }
    try {
      const savedGroupBy = localStorage.getItem(groupByStorageKey());
      const savedGroupById = savedGroupBy ? parseInt(savedGroupBy, 10) : null;
      if (savedGroupById) {
        groupByItemTypeId = savedGroupById;
      }
      const savedExcludeRightmost = localStorage.getItem(excludeRightmostSwimlaneParentsStorageKey());
      if (savedExcludeRightmost !== null) {
        excludeRightmostSwimlaneParents = savedExcludeRightmost === 'true';
      }
    } catch (e) { /* ignore storage errors */ }
    loading = false;
  });

  $effect(() => {
    if (groupByItemTypeId && itemTypes.length > 0 && !itemTypes.some(type => type.id === groupByItemTypeId)) {
      setGroupByItemType(null);
    }
  });

  // Keep backlog count in sync
  $effect(() => {
    backlogStore.setCount(workspaceId, collectionStore.backlogPagination?.total ?? collectionStore.backlogItems.length);
  });

  // Drop cached item links when the viewed collection/workspace changes so a
  // fresh board doesn't show stale dependency summaries from a previous view.
  // This runs synchronously (before any in-flight link fetch resolves), and the
  // fetch is token-guarded, so a stale request from the prior view can't write
  // back after the wipe.
  let viewSignature = $derived(`${collectionId ?? ''}|${workspaceId ?? ''}`);
  $effect(() => {
    // Re-runs whenever the viewed collection/workspace changes. Board config
    // depends only on collection/workspace, not on the item set — loading it
    // here (instead of in the items effect below) stops a redundant
    // getBoardConfiguration request on every item array update.
    viewSignature;
    dependencyLinksByItem = {};
    if (collectionId || workspaceId) {
      loadBoardConfig();
    }
  });

  // Preload transitions + dependency links when the loaded item set changes.
  $effect(() => {
    if (collectionStore.items.length > 0 && !collectionStore.loading) {
      if (workspaceId) {
        statusTransitionStore.initialize(workspaceId);
      }
      statusTransitionStore.preloadForItems([...collectionStore.items, ...collectionStore.backlogItems]);
      // untrack: the cache read inside loadDependencyLinksForItems would
      // otherwise subscribe this effect to dependencyLinksByItem and re-run it
      // (re-running preloadForItems) every time links resolve.
      untrack(() => loadDependencyLinksForItems(collectionStore.items));
    }
  });

  // Fetch item links for the dependency/blocker hover summary, caching per
  // item id. Only items we haven't already loaded links for are requested,
  // so re-renders after drag/filter/sort stay cheap. Outgoing + incoming are
  // merged the same way the item-detail store does so the link shape matches
  // what DependencySummary expects.
  async function loadDependencyLinksForItems(items) {
    const toFetch = items.filter((i) => i?.id != null && !dependencyLinksByItem[i.id]);
    if (toFetch.length === 0) return;
    const token = ++dependencyLinksToken;
    const ids = toFetch.map((i) => i.id);
    // One batched request per chunk instead of one per card — a board render
    // used to fire N concurrent /items/{id}/links requests. Chunk under the
    // server's 500-id cap.
    const chunks = [];
    for (let i = 0; i < ids.length; i += DEPENDENCY_LINK_CHUNK) {
      chunks.push(ids.slice(i, i + DEPENDENCY_LINK_CHUNK));
    }
    const groupsPerChunk = await Promise.all(
      chunks.map(async (chunk) => {
        try {
          return await api.links.getForItems(chunk);
        } catch {
          return {};
        }
      })
    );
    if (token !== dependencyLinksToken) return; // a newer load superseded us
    const next = { ...dependencyLinksByItem };
    // Seed every requested id so items with no links are cached and not re-fetched.
    for (const id of ids) next[id] = next[id] ?? [];
    for (const groups of groupsPerChunk) {
      for (const [id, group] of Object.entries(groups)) {
        const all = [];
        if (group?.outgoing) all.push(...group.outgoing);
        if (group?.incoming) all.push(...group.incoming);
        next[id] = all;
      }
    }
    dependencyLinksByItem = next;
  }

  async function loadBoardConfig() {
    try {
      boardConfig = await api.collections.getBoardConfiguration(collectionId, workspaceId);
    } catch (error) {
      if (error.status !== 404) {
        console.error('Failed to load board configuration:', error);
      }
      boardConfig = null;
    }
  }

  // Adaptive polling for board items: use cheap deltas, falling back to full refresh only when needed.
  const poller = useWorkItemPoller(() => refreshCollectionDeltas());

  // Instant refresh after an AI chat agent run — surfaces tool-call effects
  // (created items, status transitions, etc.) without waiting for the poll.
  $effect(() => agentRuns.subscribe(() => {
    reloadCollection();
  }));

  // Iteration filter derived values
  let activeLocalIteration = $derived(allIterations.find(i => !i.is_global && i.status === 'active'));

  let iterationFilterOptions = $derived.by(() => {
    const seen = new Set();
    return allIterations.filter(i => {
      if (i.status === 'completed' || i.status === 'cancelled') return false;
      if (seen.has(i.id)) return false;
      seen.add(i.id);
      return true;
    });
  });

  let filteredItems = $derived.by(() => {
    let nextItems = iterationFilterId
      ? items.filter(item => item.iteration_id === iterationFilterId)
      : items;

    if (!searchQuery.trim()) return nextItems;

    const query = searchQuery.toLowerCase();
    return nextItems.filter(item => {
      if (item.title?.toLowerCase().includes(query)) return true;
      if (item.description?.toLowerCase().includes(query)) return true;
      const itemKey = `${item.workspace_key || ''}-${item.workspace_item_number}`.toLowerCase();
      if (itemKey.includes(query)) return true;
      return false;
    });
  });

  function getItemsByStatus(statusId, itemSubset = filteredItems) {
    return itemSubset.filter(item => item.status_id === statusId);
  }

  function getItemsByColumn(column, itemSubset = filteredItems) {
    return itemSubset.filter(item => column.status_ids && column.status_ids.includes(item.status_id));
  }

  function parseLaneParentId(value) {
    if (!groupByItemTypeId) return undefined;
    if (value == null || value === '') return null;
    const parsed = parseInt(value, 10);
    return Number.isFinite(parsed) ? parsed : null;
  }

  function isExcludedRightmostSwimlaneParent(item) {
    return Boolean(excludeRightmostSwimlaneParents && rightmostBoardColumnStatusIds.has(item.status_id));
  }

  function isEligibleSwimlaneParent(item) {
    return item.item_type_id === groupByItemTypeId && !isExcludedRightmostSwimlaneParent(item);
  }

  function getItemsForLaneParent(parentId) {
    if (!groupByItemTypeId) return filteredItems;
    const parentIds = new Set(items
      .filter(isEligibleSwimlaneParent)
      .map(item => item.id));

    if (parentId != null) {
      return filteredItems.filter(item => item.parent_id === parentId && item.id !== parentId);
    }

    return filteredItems.filter(item => {
      if (item.item_type_id === groupByItemTypeId) return false;
      if (item.parent_id && parentIds.has(item.parent_id)) return false;
      return true;
    });
  }

  function wouldChangeLaneParent(item, targetParentId) {
    if (!groupByItemTypeId || targetParentId === undefined) return false;
    return (item.parent_id ?? null) !== targetParentId;
  }

  function warnUnsupportedCombinedBoardMove() {
    warningToast('Moving between swimlanes and statuses at the same time is not supported yet. Move it in two steps.');
  }

  async function updateItemParentForLane(item, targetParentId) {
    if (!groupByItemTypeId || targetParentId === undefined) return item;

    const currentParentId = item.parent_id ?? null;
    if (currentParentId === targetParentId) return item;

    try {
      const updatedItem = await api.items.update(item.id, { parent_id: targetParentId });
      collectionStore.items = collectionStore.items.map(existing =>
        existing.id === item.id
          ? { ...existing, ...updatedItem, parent_id: targetParentId }
          : existing
      );
      return { ...item, ...updatedItem, parent_id: targetParentId };
    } catch (error) {
      console.error('Failed to move item to swimlane:', error);
      warningToast('Could not move item to that swimlane');
      if (error && typeof error === 'object') {
        error.swimlaneMoveFailed = true;
      }
      throw error;
    }
  }

  // Status badges use an accessible-contrast pass so colours read against the
  // gradient backdrop on the board; the other iteration picker call sites
  // don't need this and keep the simpler hex+15 default.
  const iterationPickerConfig = buildIterationPickerConfig({
    statusBadgeColors: ({ hex }) => {
      const visible = getVisibleColor(hex);
      const { r, g, b } = hexToRgb(visible);
      return {
        bgColor: `rgba(${r}, ${g}, ${b}, 0.15)`,
        textColor: visible,
      };
    },
  });

  let otherIterationOptions = $derived(iterationFilterOptions.filter(i => i.id !== activeLocalIteration?.id));

  let selectedOtherIteration = $derived(
    iterationFilterId && iterationFilterId !== activeLocalIteration?.id
      ? allIterations.find(i => i.id === iterationFilterId)
      : null
  );

  function setIterationFilter(iterationId) {
    iterationFilterId = iterationId;
    if (iterationId) {
      localStorage.setItem(`board-iteration-filter-${workspaceId}`, String(iterationId));
    } else {
      localStorage.removeItem(`board-iteration-filter-${workspaceId}`);
    }
  }

  function boardPreferenceScope() {
    return collectionId ? `collection-${collectionId}` : `workspace-${workspaceId || 'global'}`;
  }

  function groupByStorageKey() {
    return `board-group-by-item-type-${boardPreferenceScope()}`;
  }

  function excludeRightmostSwimlaneParentsStorageKey() {
    return `board-exclude-rightmost-swimlane-parents-${boardPreferenceScope()}`;
  }

  function setGroupByItemType(itemTypeId) {
    groupByItemTypeId = itemTypeId;
    swimlaneCollapsed = {};
    try {
      if (itemTypeId) {
        localStorage.setItem(groupByStorageKey(), String(itemTypeId));
      } else {
        localStorage.removeItem(groupByStorageKey());
      }
    } catch (e) { /* ignore storage errors */ }
  }

  function setExcludeRightmostSwimlaneParents(value) {
    excludeRightmostSwimlaneParents = value;
    swimlaneCollapsed = {};
    try {
      localStorage.setItem(excludeRightmostSwimlaneParentsStorageKey(), String(value));
    } catch (e) { /* ignore storage errors */ }
  }

  function toggleSwimlane(swimlaneId) {
    swimlaneCollapsed = {
      ...swimlaneCollapsed,
      [swimlaneId]: !swimlaneCollapsed[swimlaneId]
    };
  }

  function isSwimlaneExpanded(swimlaneId) {
    return swimlaneCollapsed[swimlaneId] !== true;
  }


  // Backlog items are loaded from backend in loadData()

  // Compute display columns based on board configuration or fall back to
  // sorted statuses. Shared with collectionContext's split-fetch logic so
  // the store excludes exactly the statuses rendered in the capped column.
  let displayColumns = $derived(buildDisplayColumns(boardConfig, statuses));

  let validColumns = $derived(displayColumns.filter(col => col.status_ids?.length > 0));
  let rightmostBoardColumn = $derived(validColumns[validColumns.length - 1] ?? null);
  let rightmostBoardColumnStatusIds = $derived(new Set(rightmostBoardColumn?.status_ids || []));

  function shouldLimitRightmostColumn(columnIndex, columnsForBoard = validColumns) {
    return Boolean(boardConfig?.show_rightmost_column_last_50 && columnIndex === columnsForBoard.length - 1);
  }

  function itemRecencyValue(item) {
    return new Date(item.updated_at || item.created_at || 0).getTime() || 0;
  }

  function getDisplayItemsByColumn(column, columnIndex, columnsForBoard = validColumns, itemSubset = filteredItems) {
    const columnItems = getItemsByColumn(column, itemSubset);
    return shouldLimitRightmostColumn(columnIndex, columnsForBoard)
      ? columnItems
          .slice()
          .sort((a, b) => itemRecencyValue(b) - itemRecencyValue(a) || b.id - a.id)
          .slice(0, RIGHTMOST_COLUMN_LIMIT)
      : columnItems;
  }

  // Total item count for a column header. When the store split-fetched a
  // capped rightmost column, only the latest RIGHTMOST_COLUMN_LIMIT of its
  // items are loaded, so the loaded count undercounts — use the server-side
  // total instead. Swimlane boards keep client-derived per-lane counts (the
  // server total is board-wide, not per lane). Skipped while quick-search or
  // the iteration filter narrows the visible set — those filter the loaded
  // items, so loaded counts are the honest numbers.
  function getColumnTotal(columnIndex, allColumnItems) {
    const useServerTotal =
      collectionStore.rightmostCap &&
      !selectedGroupByItemType &&
      !searchQuery.trim() &&
      !iterationFilterId &&
      shouldLimitRightmostColumn(columnIndex);
    return useServerTotal
      ? Math.max(collectionStore.rightmostCap.total, allColumnItems.length)
      : allColumnItems.length;
  }

  let selectedGroupByItemType = $derived(
    groupByItemTypeId ? itemTypes.find(type => type.id === groupByItemTypeId) : null
  );

  let hiddenRightmostSwimlaneParentCount = $derived.by(() => {
    if (!groupByItemTypeId || !excludeRightmostSwimlaneParents || !rightmostBoardColumn) return 0;
    return items.filter(item => item.item_type_id === groupByItemTypeId && isExcludedRightmostSwimlaneParent(item)).length;
  });

  let groupByMenuItems = $derived.by(() => {
    const sortedTypes = (itemTypes || []).slice().sort((a, b) => (a.hierarchy_level ?? 999) - (b.hierarchy_level ?? 999) || (a.sort_order ?? 0) - (b.sort_order ?? 0) || a.name.localeCompare(b.name));
    const rightmostColumnName = rightmostBoardColumn?.name || 'rightmost column';
    return [
      {
        id: 'group-none',
        title: 'No swimlanes',
        subtitle: 'Show every item as a normal card',
        badge: groupByItemTypeId ? '' : 'Selected',
        onClick: () => setGroupByItemType(null)
      },
      ...(groupByItemTypeId ? [
        { id: 'group-rightmost-toggle-divider', type: 'divider' },
        {
          id: 'group-rightmost-toggle',
          type: 'checkbox',
          title: `Hide ${rightmostColumnName} swimlanes`,
          subtitle: selectedGroupByItemType
            ? `Only group by ${selectedGroupByItemType.name} items outside ${rightmostColumnName}`
            : `Only group by items outside ${rightmostColumnName}`,
          badge: hiddenRightmostSwimlaneParentCount > 0 ? `${hiddenRightmostSwimlaneParentCount} hidden` : '',
          checked: excludeRightmostSwimlaneParents,
          closeOnSelect: false,
          onChange: setExcludeRightmostSwimlaneParents
        }
      ] : []),
      ...(sortedTypes.length > 0 ? [{ id: 'group-divider', type: 'divider' }] : []),
      ...sortedTypes.map(type => {
        const TypeIcon = itemTypeIconMap[type.icon] || itemTypeIconMap.FileText;
        return {
          id: `group-type-${type.id}`,
          title: type.name,
          subtitle: 'Use these items as swimlanes',
          icon: TypeIcon,
          iconColor: type.color,
          badge: groupByItemTypeId === type.id ? 'Selected' : '',
          onClick: () => setGroupByItemType(type.id)
        };
      })
    ];
  });

  let boardSwimlanes = $derived.by(() => {
    if (!groupByItemTypeId) {
      return [{
        id: 'all',
        title: 'All items',
        parent: null,
        items: filteredItems,
        isDefault: true,
        isUnassigned: false,
        itemCount: filteredItems.length,
        parentIsVisible: false
      }];
    }

    const parentItems = items.filter(isEligibleSwimlaneParent);
    const parentIds = new Set(parentItems.map(item => item.id));
    const visibleItemIds = new Set(filteredItems.map(item => item.id));

    const lanes = parentItems
      .map(parent => {
        const laneItems = filteredItems.filter(item => item.parent_id === parent.id && item.id !== parent.id);
        return {
          id: `parent-${parent.id}`,
          title: parent.title,
          parent,
          items: laneItems,
          isDefault: false,
          isUnassigned: false,
          itemCount: laneItems.length,
          parentIsVisible: visibleItemIds.has(parent.id)
        };
      })
      .filter(lane => lane.itemCount > 0 || lane.parentIsVisible);

    const unassignedItems = filteredItems.filter(item => {
      if (item.item_type_id === groupByItemTypeId) return false;
      if (item.parent_id && parentIds.has(item.parent_id)) return false;
      return true;
    });

    return [
      ...lanes,
      {
        id: 'unassigned',
        title: 'Unassigned',
        parent: null,
        items: unassignedItems,
        isDefault: false,
        isUnassigned: true,
        itemCount: unassignedItems.length,
        parentIsVisible: false
      }
    ];
  });

  // Calculate total visible items across all display columns
  let totalVisibleItems = $derived.by(() => {
    return boardSwimlanes.reduce((laneTotal, lane) => {
      return laneTotal + validColumns.reduce((columnTotal, column, columnIndex) => {
        return columnTotal + getDisplayItemsByColumn(column, columnIndex, validColumns, lane.items).length;
      }, 0);
    }, 0);
  });

  function getStatusByName(statusName) {
    const normalizedName = statusName.toLowerCase().replace('_', ' ');
    return statuses.find(status =>
      status.name.toLowerCase() === normalizedName ||
      status.name.toLowerCase().replace(' ', '_') === statusName
    );
  }

  function getStatusColor(categoryColor) {
    // Convert hex color to Tailwind-compatible text classes
    const colorMap = {
      '#3b82f6': 'text-blue-800',
      '#ef4444': 'text-red-800',
      '#10b981': 'text-green-800',
      '#f59e0b': 'text-orange-800',
      '#6b7280': 'text-gray-800'
    };
    return colorMap[categoryColor] || 'text-gray-800';
  }

  function openItem(itemId, event) {
    // Prevent event bubbling to avoid triggering drag
    event.stopPropagation();
    selectedItemId = itemId;
    showItemModal = true;
  }

  function getMoveMenuItems(item) {
    return displayColumns
      .filter(column => column.status_ids?.length > 0 && !column.status_ids.includes(item.status_id))
      .map(column => {
        const targetStatusId = column.status_ids[0];
        const targetStatus = statuses.find(status => status.id === targetStatusId);
        const targetName = targetStatus?.name || column.name;
        const canMove = isValidTransition(item.id, item.status_id, targetStatusId);

        return {
          id: `move-${item.id}-${targetStatusId}`,
          title: canMove ? targetName : `${targetName} — not allowed`,
          iconDot: true,
          iconColor: column.color || targetStatus?.category_color || targetStatus?.color || 'var(--ds-text-subtle)',
          onClick: canMove ? () => moveItemToStatus(item, targetStatusId, targetName) : undefined,
          class: canMove ? '' : 'opacity-50 cursor-not-allowed pointer-events-none'
        };
      });
  }

  async function moveItemToStatus(item, targetStatusId, targetName) {
    if (!isValidTransition(item.id, item.status_id, targetStatusId)) {
      warningToast(t('collections.transition_failed'));
      return;
    }

    try {
      await api.items.transition(item.id, targetStatusId);
      boardAnnouncement = `Moved ${item.title} to ${targetName}`;
      successToast(boardAnnouncement);
      reloadCollection();
    } catch (err) {
      console.error('Status transition failed:', err);
      warningToast(t('collections.transition_failed'));
    }
  }

  async function closeItemModal(event) {
    showItemModal = false;
    selectedItemId = null;

    // If changes were made in the modal, reload data
    if (event?.hasChanges) {
      reloadCollection();
    }
  }

  // Drag and drop setup using Pragmatic DnD
  function setupDragAndDrop() {
    // Clear any pending setup
    if (setupTimeout) {
      clearTimeout(setupTimeout);
    }

    // Clean up existing registrations
    setupElements.forEach((cleanup, elementId) => {
      if (typeof cleanup === 'function') {
        cleanup();
      }
    });
    setupElements.clear();

    // Reset drag state
    dragState = new Map();

    // Setup work item cards as both draggable and drop targets with edge detection
    const itemCards = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-item-card]'));

    itemCards.forEach(element => {
      const itemId = parseInt(element.dataset.itemId);
      const elementId = `item-${itemId}`;

      const item = items.find(i => i.id === itemId);
      if (!item) return;
      const targetLaneParentId = parseLaneParentId(element.dataset.swimlaneParentId);

      // Initialize drag state for this item
      dragState.set(itemId, { isDragging: false, closestEdge: null });

      // Make draggable
      const draggableCleanup = draggable({
        element,
        getInitialData: () => ({
          item,
          type: 'work-item'
        }),
        onDragStart: () => {
          element.style.opacity = '0.5';
          document.body.classList.add('is-dragging');
          // Mark this item as being dragged
          const state = dragState.get(itemId) || {};
          dragState.set(itemId, { ...state, isDragging: true });
          dragState = new Map(dragState); // Trigger reactivity
        },
        onDrop: () => {
          element.style.opacity = '';
          document.body.classList.remove('is-dragging');
          // Reset all drag states
          dragState.forEach((state, id) => {
            dragState.set(id, { isDragging: false, closestEdge: null });
          });
          dragState = new Map(dragState); // Trigger reactivity
          // Reset all column border styles
          resetAllColumnStyles();
        }
      });

      // Make drop target with edge detection
      const dropTargetCleanup = dropTargetForElements({
        element,
        canDrop: ({ source }) => {
          const data = /** @type {any} */ (source.data);
          // Can't drop on self
          if (data.type !== 'work-item' || data.item.id === itemId) {
            return false;
          }

          // If items are in different status columns, validate the transition
          const sourceStatus = getStatusByItemId(data.item.id);
          const targetStatus = getStatusByItemId(itemId);

          if (sourceStatus && targetStatus && sourceStatus.id !== targetStatus.id) {
            // Different statuses - check if transition is valid
            return isValidTransition(data.item.id, sourceStatus.id, targetStatus.id);
          }

          // Same status or no status info - allow reordering
          return true;
        },
        getData: ({ input, element }) => {
          return attachClosestEdge({}, {
            input,
            element,
            allowedEdges: ['top', 'bottom']
          });
        },
        onDragEnter: ({ self, source }) => {
          const data = /** @type {any} */ (source.data);
          if (data.type === 'work-item' && data.item.id !== itemId) {
            const closestEdge = extractClosestEdge(self.data);
            const state = dragState.get(itemId) || {};
            dragState.set(itemId, { ...state, closestEdge });
            dragState = new Map(dragState); // Trigger reactivity
          }
        },
        onDragLeave: () => {
          const state = dragState.get(itemId) || {};
          dragState.set(itemId, { ...state, closestEdge: null });
          dragState = new Map(dragState); // Trigger reactivity
        },
        onDrop: ({ self, source }) => {
          const data = /** @type {any} */ (source.data);
          const closestEdge = extractClosestEdge(self.data);

          if (data.type === 'work-item' && closestEdge) {
            const targetStatus = getStatusByItemId(itemId);
            if (targetStatus) {
              handleEdgeBasedDrop(data.item, item, closestEdge, targetStatus, targetLaneParentId);
            }
          }
        }
      });

      setupElements.set(elementId, () => {
        draggableCleanup();
        dropTargetCleanup();
      });
    });

    // Setup status columns as drop targets
    const statusColumns = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-status-column]'));

    statusColumns.forEach(element => {
      const statusId = parseInt(element.dataset.statusId);
      const elementId = element.dataset.statusColumnKey || `status-${statusId}`;

      const status = statuses.find(s => s.id === statusId);
      if (!status) return;
      const targetLaneParentId = parseLaneParentId(element.dataset.swimlaneParentId);

      const cleanup = dropTargetForElements({
        element,
        canDrop: ({ source }) => {
          // Allow all work items to enter so we can show valid/invalid feedback
          // Actual validation happens in onDrop
          return (/** @type {any} */ (source.data)).type === 'work-item';
        },
        onDragEnter: ({ source }) => {
          const data = /** @type {any} */ (source.data);
          if (data.type === 'work-item') {
            if (isValidTransition(data.item.id, data.item.status_id, statusId)) {
              // Valid drop - use inset shadow for highlight (preserve column border colors)
              element.style.boxShadow = 'inset 0 0 0 2px var(--ds-border-focused)';
            } else {
              // Invalid drop - use inset shadow for highlight (preserve column border colors)
              element.style.boxShadow = 'inset 0 0 0 2px var(--ds-border-danger)';
            }
          }
        },
        onDragLeave: () => {
          // Reset styles
          element.style.boxShadow = '';
        },
        onDrop: async ({ source, location }) => {
          // Reset all column styles immediately
          resetAllColumnStyles();

          const data = /** @type {any} */ (source.data);
          if (data.type === 'work-item') {
            // If an inner item drop target exists, handleEdgeBasedDrop already handles status
            const dropTargets = location.current.dropTargets;
            if (dropTargets.length > 1 && dropTargets[0].element !== element) {
              return;
            }
            const isSameStatus = data.item.status_id === statusId;
            if (!isSameStatus && wouldChangeLaneParent(data.item, targetLaneParentId)) {
              warnUnsupportedCombinedBoardMove();
              return;
            }
            if (!isSameStatus && !isValidTransition(data.item.id, data.item.status_id, statusId)) {
              warningToast(t('collections.transition_failed'));
              return;
            }

            try {
              let droppedItem = data.item;
              if (!isSameStatus) {
                droppedItem = await api.items.transition(data.item.id, statusId);
              }
              await updateItemParentForLane(droppedItem, targetLaneParentId);
            } catch (err) {
              console.error('Board drop failed:', err);
              if (!err?.swimlaneMoveFailed) {
                warningToast(t('collections.transition_failed'));
              }
            }
            reloadCollection();
          }
        }
      });

      setupElements.set(elementId, cleanup);
    });

    // No longer using position drop zones - edge detection handles everything
  }

  // Helper functions
  function resetAllColumnStyles() {
    // Reset all status column styles to their default state
    const statusColumns = /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-status-column]'));
    statusColumns.forEach(element => {
      element.style.boxShadow = '';
    });
  }

  function getStatusByItemId(itemId) {
    const item = items.find(i => i.id === itemId);
    if (!item || !item.status_id) return null;
    return statuses.find(s => s.id === item.status_id);
  }

  // Check if a status transition is valid for an item (synchronous, uses cached store data)
  function isValidTransition(itemId, fromStatusId, toStatusId) {
    if (!fromStatusId || !toStatusId) return false;
    if (fromStatusId === toStatusId) return true;
    const item = items.find(i => i.id === itemId)
      || backlogItems.find(i => i.id === itemId);
    return statusTransitionStore.isValidTransition(item?.item_type_id ?? null, fromStatusId, toStatusId);
  }

  async function updateItemStatus(itemId, newStatus) {
    try {
      await api.items.update(itemId, { status: newStatus });

      // Update store directly with a new array to ensure reactivity
      collectionStore.items = collectionStore.items.map(item =>
        item.id === itemId
          ? { ...item, status: newStatus }
          : item
      );

      // Force a re-setup of drag and drop with the updated items
      setTimeout(() => {
        setupDragAndDrop();
      }, 100);
    } catch (error) {
      console.error('Failed to update item status:', error);
      // Could add user notification here
    }
  }

  async function handleEdgeBasedDrop(draggedItem, targetItem, closestEdge, targetStatus, targetLaneParentId = undefined) {
    // Create a unique identifier for this drop operation
    const dropId = `${draggedItem.id}-edge-${targetItem.id}-${closestEdge}`;

    try {
      // Prevent duplicate drops
      if (pendingDrops.has(dropId)) {
        return;
      }

      pendingDrops.add(dropId);

      // Reset all column border styles immediately
      resetAllColumnStyles();

      const currentStatusId = draggedItem.status_id;
      const targetStatusId = targetStatus.id;

      // Check if we need to update status
      const isSameStatus = currentStatusId === targetStatusId;

      if (!isSameStatus && wouldChangeLaneParent(draggedItem, targetLaneParentId)) {
        warnUnsupportedCombinedBoardMove();
        reloadCollection();
        return;
      }

      if (!isSameStatus && !isValidTransition(draggedItem.id, currentStatusId, targetStatusId)) {
        warningToast(t('collections.transition_failed'));
        reloadCollection();
        return;
      }

      // If changing status, update the status first
      if (!isSameStatus) {
        try {
          const transitionedItem = await api.items.transition(draggedItem.id, targetStatusId);
          draggedItem = { ...draggedItem, ...transitionedItem, status_id: targetStatusId };
        } catch (err) {
          console.error('Status transition failed:', err);
          warningToast(t('collections.transition_failed'));
          reloadCollection();
          return;
        }

        // Update store directly for the status change
        collectionStore.items = collectionStore.items.map(item =>
          item.id === draggedItem.id
            ? { ...item, status_id: targetStatusId }
            : item
        );
      }

      draggedItem = await updateItemParentForLane(draggedItem, targetLaneParentId);

      // Get items in the target status for position calculation
      const laneItems = targetLaneParentId === undefined ? filteredItems : getItemsForLaneParent(targetLaneParentId);
      const statusItems = getItemsByStatus(targetStatusId, laneItems);

      // Find the target item's position in the sorted status items
      const targetIndex = statusItems.findIndex(item => item.id === targetItem.id);
      const draggedIndex = statusItems.findIndex(item => item.id === draggedItem.id);

      // Remove the dragged item from consideration to get accurate neighboring items
      const otherItems = statusItems.filter(item => item.id !== draggedItem.id);
      const adjustedTargetIndex = otherItems.findIndex(item => item.id === targetItem.id);

      // Check if we're trying to drop in the same position
      const isDroppingSamePosition = (
        (closestEdge === 'top' && draggedIndex === targetIndex - 1) ||
        (closestEdge === 'bottom' && draggedIndex === targetIndex + 1)
      );

      if (isDroppingSamePosition && isSameStatus) {
        return;
      }

      // Calculate item IDs based on edge (backend will determine actual global ranks)
      let prevItemId = null;
      let nextItemId = null;

      if (closestEdge === 'top') {
        // Insert before target item
        if (adjustedTargetIndex > 0) {
          const prevItem = otherItems[adjustedTargetIndex - 1];
          if (prevItem) prevItemId = prevItem.id;
        }
        if (targetItem) nextItemId = targetItem.id;
      } else if (closestEdge === 'bottom') {
        // Insert after target item
        if (targetItem) prevItemId = targetItem.id;
        if (adjustedTargetIndex < otherItems.length - 1) {
          const nextItem = otherItems[adjustedTargetIndex + 1];
          if (nextItem) nextItemId = nextItem.id;
        }
      }

      // Update the frac_index using item IDs
      const indexData = {
        prev_item_id: prevItemId,
        next_item_id: nextItemId
      };
      const updatedItem = await api.items.updateFracIndex(draggedItem.id, indexData);

      // Reload data from central store to get the correct ordering
      reloadCollection();

    } catch (error) {
      console.error('Failed to handle edge-based drop:', error);
      console.error('Error details:', error.message);

      // If we get a rank ordering error, reload fresh data
      if (error.status === 500) {
        reloadCollection();
      }
    } finally {
      // Always remove from pending drops
      setTimeout(() => {
        pendingDrops.delete(dropId);
      }, 500); // Small delay to prevent rapid re-triggering
    }
  }


  // Setup drag and drop when data changes. Track grouping/swimlane inputs too,
  // because switching group-by replaces the board DOM without changing item count.
  $effect(() => {
    const itemSignature = items.map(item => `${item.id}:${item.status_id ?? ''}:${item.parent_id ?? ''}`).join('|');
    const laneSignature = boardSwimlanes.map(lane => `${lane.id}:${lane.itemCount}:${isSwimlaneExpanded(lane.id)}`).join('|');
    const columnSignature = validColumns.map(column => `${column.id}:${(column.status_ids || []).join(',')}`).join('|');
    groupByItemTypeId;
    itemSignature;
    laneSignature;
    columnSignature;

    if (items.length > 0 && statuses.length > 0 && typeof document !== 'undefined') {
      if (setupTimeout) clearTimeout(setupTimeout);
      setupTimeout = setTimeout(() => {
        setupDragAndDrop();
      }, 100);
    }
  });

</script>

{#if loading}
  <div class="p-6">
    <div class="animate-pulse">{t('common.loading')}</div>
  </div>
{:else if workspace || !workspaceId}
  <StaticViewBackground
    backgroundStyle={styles.backgroundStyle}
    contextVars={styles.contextVars}
    contentClass="p-6 min-w-fit"
  >
    <!-- Content Container -->
      <!-- Header with view tabs -->
      <div class="mb-8">
        <ViewHeader
          workspaceName={workspace?.name || ''}
          collection={currentCollectionName}
          viewName="Board"
          itemCount={(collectionStore.itemsPagination?.total ?? filteredItems.length) + (collectionStore.rightmostCap?.total ?? 0)}
        >
          {#snippet actions()}
            <div class="flex items-center gap-3">
              {#if allIterations.length > 0}
                <div class="inline-flex items-center rounded-lg border overflow-hidden text-sm"
                     style="background-color: var(--ctx-surface, transparent); backdrop-filter: var(--ctx-backdrop, none); border-color: var(--ctx-border, var(--ds-border));">
                  <button
                    class="px-3 py-1.5 transition-colors"
                    style={!iterationFilterId
                      ? 'background-color: var(--ctx-surface-raised, var(--ds-surface-raised)); color: var(--ds-text); font-weight: 500;'
                      : 'color: var(--ds-text); background-color: transparent;'}
                    onclick={() => setIterationFilter(null)}
                  >
                    {t('collections.allItems')}
                  </button>
                  {#if activeLocalIteration}
                    <button
                      class="px-3 py-1.5 transition-colors border-l"
                      style="border-color: var(--ctx-border, var(--ds-border)); {iterationFilterId === activeLocalIteration.id
                        ? 'background-color: var(--ctx-surface-raised, var(--ds-surface-raised)); color: var(--ds-text); font-weight: 500;'
                        : 'color: var(--ds-text); background-color: transparent;'}"
                      onclick={() => setIterationFilter(activeLocalIteration.id)}
                    >
                      {activeLocalIteration.name}
                    </button>
                  {/if}
                  {#if otherIterationOptions.length > 0}
                    <ItemPicker
                      items={otherIterationOptions}
                      value={iterationFilterId && iterationFilterId !== activeLocalIteration?.id ? iterationFilterId : null}
                      config={iterationPickerConfig}
                      placeholder={t('iterations.filterByIteration')}
                      showUnassigned={false}
                      allowClear={false}
                      showSelectedInTrigger={false}
                      onSelect={(iter) => {
                        if (iter) {
                          setIterationFilter(iter.id);
                        }
                      }}
                    >
                      {#snippet children()}
                        <span
                          class="px-3 py-1.5 text-sm border-l flex items-center gap-1 transition-colors"
                          style="border-color: var(--ctx-border, var(--ds-border)); {selectedOtherIteration
                            ? 'color: var(--ds-text); font-weight: 500; background-color: var(--ctx-surface-raised, var(--ds-surface-raised));'
                            : 'color: var(--ds-text);'}"
                        >
                          {selectedOtherIteration ? selectedOtherIteration.name : t('iterations.filterByIteration')}
                          <ChevronDown size={12} />
                        </span>
                      {/snippet}
                    </ItemPicker>
                  {/if}
                </div>
              {/if}
              <DropdownMenu
                items={groupByMenuItems}
                triggerIcon={Layers}
                triggerText={selectedGroupByItemType ? `Group by: ${selectedGroupByItemType.name}` : 'Group by'}
                placement="bottom-end"
                maxWidth="max-w-xs"
                triggerClass="px-3.5 py-1.5 rounded border text-sm font-medium"
                triggerStyle="background-color: var(--ctx-surface, var(--ds-surface-raised)); color: var(--ds-text); border-color: var(--ctx-border, var(--ds-border)); backdrop-filter: var(--ctx-backdrop, none);"
                triggerTestid="board-group-by-menu"
              />
              <CollectionViewSwitcher
                {workspaceId}
                {collectionId}
                activeView="board"
                publicSlug={collectionStore.publicSlug}
              />
            </div>
          {/snippet}
        </ViewHeader>
      </div>

      <!-- Controls Bar -->
      <div class="flex items-center gap-4 mb-6">
        <SearchInput
          bind:value={searchQuery}
          placeholder={t('common.search')}
        />
        <SubFilterBar {workspaceId} />
      </div>

      <div class="sr-only" aria-live="polite">{boardAnnouncement}</div>

      {#if statuses.length === 0}
        <!-- No Statuses State -->
        <div class="text-center py-12">
          <div class="mb-4" style="color: var(--ctx-text-subtlest, var(--ds-icon-disabled));">
            <Plus class="w-16 h-16 mx-auto" />
          </div>
          <h3 class="text-lg font mb-2" style="color: var(--ctx-text, var(--ds-text));">{t('items.noItemsInFilter')}</h3>
          <p class="text-sm mb-4" style="color: var(--ctx-text-subtle, var(--ds-text-subtle));">
            {t('items.createToStart')}
          </p>
          <button
            onclick={() => navigate('/admin/workflows')}
            class="px-4 py-2 bg-blue-600 text-white rounded hover:bg-blue-700 transition-colors"
          >
            {t('statuses.createStatus')}
          </button>
        </div>
      {:else}
        <!-- Board Columns / Swimlanes -->
        <div class={selectedGroupByItemType ? 'space-y-4' : ''} data-testid="board-view">
          {#each boardSwimlanes as lane (lane.id)}
            {@const laneExpanded = isSwimlaneExpanded(lane.id)}
            {@const LaneTypeIcon = selectedGroupByItemType ? (itemTypeIconMap[selectedGroupByItemType.icon] || itemTypeIconMap.FileText) : null}
            <section
              class={selectedGroupByItemType ? 'rounded-lg border overflow-hidden' : ''}
              style={selectedGroupByItemType ? `${styles.glassStyle?.(10) ?? ''} border-color: var(--ctx-border, var(--ds-border));` : ''}
              data-board-swimlane={lane.id}
            >
              {#if selectedGroupByItemType}
                <div class="flex items-center justify-between gap-3 px-3 py-2 border-b" style="border-color: var(--ctx-border, var(--ds-border));">
                  <Button
                    variant="ghost"
                    size="sm"
                    class="flex-1 justify-start px-2"
                    style="color: var(--ds-text);"
                    onclick={() => toggleSwimlane(lane.id)}
                  >
                    {#if laneExpanded}
                      <ChevronDown class="w-4 h-4 flex-shrink-0" />
                    {:else}
                      <ChevronRight class="w-4 h-4 flex-shrink-0" />
                    {/if}
                    {#if LaneTypeIcon}
                      <span
                        class="w-5 h-5 rounded flex items-center justify-center text-white flex-shrink-0"
                        style="background-color: {lane.isUnassigned ? 'var(--ds-background-neutral-bold, #6b7280)' : selectedGroupByItemType.color};"
                      >
                        <LaneTypeIcon class="w-3 h-3" />
                      </span>
                    {/if}
                    <span class="min-w-0 flex-1 text-left">
                      <span class="block font-semibold truncate">{lane.title}</span>
                      {#if lane.parent}
                        <span class="block text-xs font-normal truncate" style="color: var(--ds-text-subtle);">
                          <ItemKey item={lane.parent} {workspace} /> · {selectedGroupByItemType.name} swimlane
                        </span>
                      {:else}
                        <span class="block text-xs font-normal truncate" style="color: var(--ds-text-subtle);">
                          Items without {excludeRightmostSwimlaneParents ? 'a visible' : 'a'} {selectedGroupByItemType.name} parent
                        </span>
                      {/if}
                    </span>
                  </Button>
                  <span class="text-xs px-2 py-0.5 rounded-full flex-shrink-0" style="background: var(--ds-background-neutral); color: var(--ds-text-subtle);">
                    {lane.itemCount} {lane.itemCount === 1 ? 'item' : 'items'}
                  </span>
                </div>
              {/if}

              {#if !selectedGroupByItemType || laneExpanded}
                <div class="grid gap-6 {selectedGroupByItemType ? 'p-4' : ''}" style="grid-template-columns: repeat({validColumns.length}, minmax(300px, 1fr));">
                  {#each validColumns as column, columnIndex (column.id)}
                    {@const quickAddKey = `${lane.id}-${column.id}`}
                    {@const allColumnItems = getItemsByColumn(column, lane.items)}
                    {@const columnItems = getDisplayItemsByColumn(column, columnIndex, validColumns, lane.items)}
                    {@const columnTotal = getColumnTotal(columnIndex, allColumnItems)}
                    {@const hiddenColumnItemCount = columnTotal - columnItems.length}
                    {@const isOverWip = column.wip_limit && allColumnItems.length > column.wip_limit}
                    <div
                      class="relative rounded border shadow-sm transition-colors"
                      style="{styles.columnStyle(12)} {quickAddState[quickAddKey]?.show ? 'z-index: 30;' : ''}"
                      data-testid="board-column"
                      data-status-column
                      data-status-column-key={`${lane.id}-${column.id}-${column.status_ids[0]}`}
                      data-swimlane-parent-id={selectedGroupByItemType && lane.parent ? lane.parent.id : ''}
                      data-status-id={column.status_ids[0]}
                    >
                      <div class="p-4 border-b border-t-4" style="border-bottom-color: var(--ctx-border, var(--ds-border)); border-top-color: {column.color};">
                        <div class="flex items-center justify-between">
                          <h3 class="font-semibold" data-testid="column-header" style={styles.glassTextStyle}>{column.name}</h3>
                          <button
                            onclick={() => initQuickAdd(column.id, column.status_ids[0], quickAddKey, lane.parent?.id ?? null)}
                            class="p-1 rounded transition-colors"
                            style="color: var(--ds-text-subtle);"
                            onmouseenter={(e) => e.currentTarget.style.color = 'var(--ds-text)'}
                            onmouseleave={(e) => e.currentTarget.style.color = 'var(--ds-text-subtle)'}
                            title={t('collections.addCard')}
                          >
                            <Plus class="w-4 h-4" />
                          </button>
                        </div>
                        <div class="flex items-center justify-between">
                          <span class="text-sm" style={styles.glassSubtleTextStyle}>
                            {#if hiddenColumnItemCount > 0}
                              {columnItems.length} of {columnTotal} {t('items.item')}
                            {:else}
                              {columnTotal} {t('items.item')}
                            {/if}
                          </span>
                          {#if column.wip_limit}
                            <span class="text-xs px-2 py-0.5 rounded"
                                  style={isOverWip
                                    ? 'background-color: var(--ds-danger-subtle); color: var(--ds-text-danger);'
                                    : 'background-color: var(--ds-background-neutral, #091e420f); color: var(--ds-text-subtle, #6b778c);'}>
                              WIP: {allColumnItems.length}/{column.wip_limit}
                            </span>
                          {/if}
                        </div>
                      </div>
                      <div class="p-4 min-h-32">
                        {#if quickAddState[quickAddKey]?.show}
                          <div class="mb-3">
                            <QuickAddForm
                              parentId={quickAddKey}
                              formState={quickAddState[quickAddKey]}
                              {workspaces}
                              cardBgStyle={styles.cardStyle(8)}
                              onUpdateField={updateQuickAddField}
                              onCreate={createColumnItem}
                              onCancel={cancelQuickAdd}
                            />
                          </div>
                        {/if}
                        {#if columnItems.length === 0 && !quickAddState[quickAddKey]?.show}
                          <!-- Empty column state -->
                          <div class="text-center py-8" style={styles.glassSubtleTextStyle}>
                            <Plus class="w-8 h-8 mx-auto mb-2" />
                            <p class="text-sm">{t('items.noItems')}</p>
                          </div>
                        {:else}
                          {#if hiddenColumnItemCount > 0}
                            <p class="text-xs mb-3" style={styles.glassSubtleTextStyle}>
                              Showing latest {columnItems.length} of {columnTotal} items in this column.
                            </p>
                          {/if}
                          <div class="space-y-1">
                            {#each columnItems as item, index (item.id)}
                              {@const moveMenuItems = getMoveMenuItems(item)}
                              <!-- Item card with edge-based drop detection -->
                              <div
                                class="relative border rounded px-3 py-3 board-card"
                                style:box-shadow="var(--ds-shadow-raised)"
                                style="{styles.cardStyle(4)}"
                                data-item-card
                                data-item-id={item.id}
                                data-swimlane-parent-id={selectedGroupByItemType && lane.parent ? lane.parent.id : ''}
                                role="button"
                                tabindex="0"
                                onclick={event => openItem(item.id, event)}
                                onkeydown={event => (event.key === 'Enter' || event.key === ' ') && openItem(item.id, event)}
                              >
                                <!-- Drop indicator -->
                                {#if dragState.get(item.id)?.closestEdge}
                                  <DropIndicator edge={dragState.get(item.id)?.closestEdge} />
                                {/if}

                                <div class="cursor-grab active:cursor-grabbing">
                                  <!-- Content -->
                                  <div class="min-w-0">
                                    <div class="flex items-start gap-2 mb-2">
                                      <!-- Title - allows wrapping -->
                                      <h4 class="text-sm leading-snug break-words flex-1 min-w-0" style={styles.glassTextStyle}>
                                        {item.title}
                                      </h4>
                                      <div class="shrink-0 -mt-1 -mr-1 opacity-80 transition-opacity board-card-menu">
                                        <DropdownMenu
                                          items={moveMenuItems.length > 0 ? moveMenuItems : [{ id: `no-moves-${item.id}`, type: 'text', text: 'No available moves' }]}
                                          placement="bottom-end"
                                          maxWidth="max-w-xs"
                                          triggerIcon={MoreHorizontal}
                                          triggerClass="p-1 rounded hover:bg-[var(--ds-background-neutral-hovered)] focus:ring-2 focus:ring-[var(--ds-border-focused)]"
                                          triggerStyle="color: var(--ds-text-subtle);"
                                          iconOnly
                                          showChevron={false}
                                          triggerTestid={`board-card-move-menu-${item.id}`}
                                        />
                                      </div>
                                    </div>

                                    <!-- Configured card fields -->
                                    {#if cardFields.length > 0}
                                      <div class="flex flex-wrap gap-1.5 mt-1 mb-1.5">
                                        {#each cardFields as cardField}
                                          <CardFieldChip
                                            {cardField}
                                            {item}
                                            {priorities}
                                            {statuses}
                                            {iterations}
                                            {projects}
                                            labels={wdsLabels}
                                            {customFieldDefinitions}
                                            {users}
                                          />
                                        {/each}
                                      </div>
                                    {/if}

                                    <!-- Bottom row: Icon, Key, Assignee avatar -->
                                    <div class="flex items-center gap-2 min-h-5">
                                      {#if item.item_type_id && itemTypes.length > 0}
                                        {@const itemType = itemTypes.find(type => type.id === item.item_type_id)}
                                        {#if itemType}
                                          {@const TypeIcon = itemTypeIconMap[itemType.icon] || itemTypeIconMap.FileText}
                                          <div
                                            class="w-4 h-4 rounded flex items-center justify-center text-white text-xs flex-shrink-0"
                                            style="background-color: {itemType.color};"
                                            title={itemType.name}
                                          >
                                            <TypeIcon class="w-3 h-3" />
                                          </div>
                                        {/if}
                                      {/if}
                                      <ItemKey {item} {workspace} />
                                      <!-- Dependency/blocker hover summary -->
                                      <DependencySummary
                                        {item}
                                        links={dependencyLinksByItem[item.id] ?? []}
                                      />
                                      <span class="flex-1"></span>
                                      {#if item.assignee_id}
                                        {@const assignee = users.find(u => u.id === item.assignee_id)}
                                        {#if assignee}
                                          <div class="w-5 h-5 rounded-full bg-blue-500 flex items-center justify-center text-white text-[9px] font-medium flex-shrink-0"
                                               title="{assignee.first_name} {assignee.last_name}">
                                            {(assignee.first_name?.[0] || '') + (assignee.last_name?.[0] || '')}
                                          </div>
                                        {/if}
                                      {/if}
                                      {#if false && statuses.find(s => s.id === item.status_id)}
                                        {@const itemStatusObj = statuses.find(s => s.id === item.status_id)}
                                        <Tooltip content={itemStatusObj.name} placement="top">
                                          <div
                                            class="w-4 h-4 rounded flex-shrink-0"
                                            style="background-color: {itemStatusObj.category_color};"
                                          ></div>
                                        </Tooltip>
                                      {/if}
                                    </div>
                                  </div>
                                </div>
                              </div>
                            {/each}
                          </div>
                        {/if}
                      </div>
                    </div>
                  {/each}
                </div>
              {/if}
            </section>
          {/each}
        </div>

        <!-- Load More -->
        {#if collectionStore.itemsHasMore}
          <div class="mt-6 text-center">
            <button
              data-testid="board-load-more"
              onclick={() => collectionStore.loadMoreItems()}
              disabled={collectionStore.itemsLoadingMore}
              class="px-4 py-2 text-sm  rounded-lg border transition-colors"
              style="{styles.glassStyle?.(12) ?? ''} {styles.glassTextStyle ?? ''}"
            >
              {collectionStore.itemsLoadingMore ? t('common.loading') : t('common.loadMore')}
              {#if collectionStore.itemsPagination?.total && !iterationFilterId}
                ({collectionStore.itemsPagination.total - collectionStore.mainItemsLoadedCount} {t('common.remaining')})
              {/if}
            </button>
          </div>
        {/if}

        <!-- Summary -->
        <div class="mt-8 text-center">
          <p class="text-sm" style="color: var(--ctx-text-subtle, var(--ds-text-subtle));">
            {t('collections.boardSummary', { itemCount: totalVisibleItems, columnCount: displayColumns.length })}
          </p>
        </div>
      {/if}
  </StaticViewBackground>
{:else}
  <div class="p-6">
    <div class="text-center" style="color: var(--ds-text-subtle);">
      {t('workspaces.noWorkspaces')}
    </div>
  </div>
{/if}

<!-- Item Detail Modal -->
{#if showItemModal && selectedItemId}
  <ItemDetail
    workspaceId={workspaceId}
    itemId={selectedItemId}
    isModal={true}
    onclose={closeItemModal}
  />
{/if}

<style>
  /* Board card hover: uses !important to override inline background-color from cardStyle */
  .board-card {
    transition: background-color 140ms ease-in-out, box-shadow 140ms ease-in-out;
  }
  .board-card:hover {
    background-color: var(--ds-surface-raised-hovered) !important;
  }

  /* During drag, reduce opacity of non-dragged items slightly */
  :global(body.is-dragging) [data-item-card] {
    transition: opacity 0.2s ease;
  }

</style>
