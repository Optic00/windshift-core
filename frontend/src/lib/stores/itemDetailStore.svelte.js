/**
 * Store for managing Item Detail state.
 * Uses Svelte 5 class-based reactive state pattern.
 * Centralizes item data, editing states, modals, and related data loading.
 */
import { api } from '../api.js';
import { buildDetailScreenFieldConfig, resolveEffectiveScreenIds } from '../utils/screenFields.js';

const FIELD_MAP = {
  title: 'title',
  description: 'description',
  status: 'status_id',
  priority: 'priority_id',
  dueDate: 'due_date',
  startDate: 'start_date',
  endDate: 'end_date',
  milestone: 'milestones',
  iteration: 'iteration_id',
  assignee: 'assignee_id',
  project: 'project_id',
};

const STRING_FIELDS = new Set(['title', 'description']);

function isNumericID(value) {
  return /^\d+$/.test(String(value ?? ''));
}

function isAbortError(error) {
  return error?.name === 'AbortError';
}

function childItemListsMatch(current = [], next = []) {
  if (current === next) return true;
  if (!Array.isArray(current) || !Array.isArray(next) || current.length !== next.length) {
    return false;
  }

  return current.every((item, index) => childItemSummariesMatch(item, next[index]));
}

function childItemSummariesMatch(current = {}, next = {}) {
  const fields = [
    'id',
    'workspace_id',
    'workspace_key',
    'workspace_item_number',
    'item_type_id',
    'title',
    'status_id',
    'status_name',
    'status_color',
    'frac_index',
  ];

  return fields.every((field) => (current?.[field] ?? null) === (next?.[field] ?? null));
}

const RELATED_ITEM_FIELDS = {
  status: ['status_id', 'status_name', 'status_color', 'status_category_id'],
  priority: ['priority_id', 'priority_name', 'priority_color'],
  iteration: ['iteration_id', 'iteration_name', 'iteration_end_date'],
  assignee: ['assignee_id', 'assignee_name', 'assignee_email'],
  project: ['project_id', 'project_name', 'inherit_project'],
};

const DEFAULT_EDITING_STATE = {
  title: { active: false, value: '' },
  description: { active: false, value: '' },
  status: { active: false, value: null },
  priority: { active: false, value: null },
  dueDate: { active: false, value: null },
  startDate: { active: false, value: null },
  endDate: { active: false, value: null },
  milestone: { active: false, value: [] },
  iteration: { active: false, value: null },
  project: { active: false, value: null },
  assignee: { active: false, value: null },
  customFields: { active: {}, values: {} },
};

class ItemDetailStore {
  // Monotonic counter for in-flight loadItem calls; lets us discard results
  // from superseded calls when the user clicks rapidly through items.
  #loadToken = 0;
  #loadController = null;
  #linksController = null;
  #worklogsController = null;
  #worklogsPromise = null;
  #worklogsPromiseItemId = null;
  #worklogsLoadedItemId = null;
  #timeModalDataController = null;
  #timeModalDataPromise = null;
  #timeModalDataLoaded = false;

  // === Current Item ===
  item = $state(null);
  itemId = $state(null);
  workspaceId = $state(null);
  loading = $state(true);
  error = $state(null);
  saving = $state(false);
  // Set when the open item no longer exists (deleted elsewhere — discovered via
  // the SSE `deleted` event or a 404 on refresh). The detail view watches this
  // and closes/navigates away instead of showing stale data (WI-484).
  notFound = $state(false);

  // Workspace
  workspace = $state(null);

  // === Editing State (unified flag + value) ===
  editing = $state({ ...DEFAULT_EDITING_STATE });

  // === Related Data (cached) ===
  parentHierarchy = $state([]);
  childItems = $state([]);
  loadingChildItems = $state(false);
  milestones = $state([]);
  iterations = $state([]);
  priorities = $state([]);

  // Item types
  itemTypes = $state([]);
  currentItemType = $state(null);
  currentHierarchyLevel = $state(null);
  availableSubIssueTypes = $state([]);

  // Screen configuration (cached per workspace).
  // workspaceScreenFields / workspaceScreenSystemFields hold the *visibility*
  // union (edit ∪ view); editableScreenFieldIds / editableScreenSystemFields
  // are the *editable* subset (edit-screen only). When both are null, all
  // visible fields are editable (i.e., the legacy single-screen path is
  // active and there's no view-vs-edit separation in play).
  customFieldDefinitions = $state([]);
  workspaceScreenFields = $state([]);
  workspaceScreenSystemFields = $state([]);
  // Virtual field metadata for the item's request type (read-only display only).
  requestTypeFields = $state([]);
  editableScreenFieldIds = $state(null);
  editableScreenSystemFields = $state(null);

  // Status
  availableStatusTransitions = $state([]);
  loadingStatusTransitions = $state(false);
  pendingApproval = $state(null);

  // Links
  itemLinks = $state([]);
  linkTypes = $state([]);
  loadingLinks = $state(false);

  // Watch
  isWatching = $state(false);
  loadingWatchStatus = $state(false);

  // Time tracking
  timeProjects = $state([]);
  timeWorklogs = $state([]);
  timeWorklogsLoading = $state(false);
  timeModalDataLoading = $state(false);
  customers = $state([]);
  workItems = $state([]);
  workspaces = $state([]);
  // Child-item rollup for the Time Tracking tab "Include child items" toggle.
  // timeRollup is null until the user opts in; it caches the API response
  // for the current item view so toggling on/off doesn't refetch.
  includeChildItems = $state(false);
  timeRollup = $state(null);
  timeRollupLoading = $state(false);

  // Diagrams & Actions
  diagrams = $state([]);
  loadingDiagrams = $state(false);
  manualActions = $state([]);

  // Modals
  showDeleteDialog = $state(false);
  showLinkModal = $state(false);
  // Preselect the link type when the add-link modal opens — set by callers
  // that pre-select a type (e.g. the Pages section's Add button).
  linkModalPreselectTypeId = $state(null);
  showTestCaseModal = $state(false);
  selectedTestCaseId = $state(null);
  showTimeLogModal = $state(false);
  editingWorklog = $state(null);

  // Track changes
  hasChanges = $state(false);

  // Animation state
  transitioning = $state(false);

  // Dropdown items (computed from item state)
  dropdownItems = $state([]);

  // === Derived Values (getters) ===

  /**
   * Get status options based on loaded transitions.
   */
  get statusOptions() {
    if (this.availableStatusTransitions.length > 0) {
      return this.availableStatusTransitions.map((transition) => ({
        id: transition.id,
        value: transition.value,
        label: transition.name,
        categoryColor: transition.category_color || null,
      }));
    }
    return this.loadingStatusTransitions ? [{ value: '', label: 'Loading...' }] : [];
  }

  /**
   * Get filtered link types (excluding test link type for item-to-item linking).
   */
  get filteredLinkTypes() {
    return this.linkTypes;
  }

  // === Data Loading Methods ===

  /**
   * Load all item data and related data.
   *
   * Stale-while-revalidate: when an item is already displayed (a switch),
   * existing state stays in place and is overwritten only as new data arrives,
   * so the UI doesn't flash a skeleton between items. Rapid clicks are made
   * race-safe by a monotonic load token; superseded results are discarded.
   */
  async loadItem(workspaceId, itemId, options = {}) {
    this.#loadController?.abort();
    this.#linksController?.abort();
    this.#worklogsController?.abort();
    const controller = new AbortController();
    this.#loadController = controller;
    const requestOptions = { signal: controller.signal };
    const token = ++this.#loadToken;
    const isSwitch = this.item != null;

    let effectiveWorkspaceId = workspaceId;
    let effectiveItemId = itemId;
    const lookupWorkspaceKey =
      options.workspaceKey || (workspaceId && !isNumericID(workspaceId) ? workspaceId : null);
    const lookupItemNumber = options.itemNumber || (lookupWorkspaceKey ? itemId : null);

    this.itemId = effectiveItemId;
    this.timeWorklogs = [];
    this.timeWorklogsLoading = false;
    this.#worklogsLoadedItemId = null;
    this.#worklogsPromise = null;
    this.#worklogsPromiseItemId = null;
    this.error = null;
    this.notFound = false;
    if (!isSwitch) {
      this.loading = true;
      this.loadingLinks = true;
    }

    try {
      let itemData = null;
      if (lookupWorkspaceKey) {
        const resolved = await api.items.getByKey(
          lookupWorkspaceKey,
          lookupItemNumber,
          requestOptions
        );
        if (token !== this.#loadToken) return;
        effectiveItemId = resolved.id;
        effectiveWorkspaceId = resolved.workspace_id;
        this.itemId = effectiveItemId;
        // The key endpoint already returns the full item-detail payload. Reuse
        // it instead of immediately requesting GET /items/{id} again.
        itemData = resolved;
      }

      // Fetch item first to derive workspaceId if not provided
      itemData ??= await api.items.get(effectiveItemId, requestOptions);
      if (token !== this.#loadToken) return;

      this.item = itemData;
      if (this.item.assignee_id === undefined) {
        this.item.assignee_id = null;
      }

      // Use provided/resolved workspaceId or derive from item
      const wsId = effectiveWorkspaceId || itemData.workspace_id;
      this.workspaceId = wsId;

      const [
        workspaceData,
        linkTypesData,
        linksData,
        customFieldsData,
        milestonesData,
        iterationsData,
        projectsData,
        requestTypeFieldsData,
      ] = await Promise.all([
        api.workspaces.get(wsId, requestOptions),
        api.linkTypes.getAll(false, requestOptions),
        api.links.getForItem('items', effectiveItemId, requestOptions),
        api.customFields.getAll({}, requestOptions),
        api.milestones.getAll({ workspace_id: wsId, include_global: true }, requestOptions),
        api.iterations.getAll({ workspace_id: wsId, include_global: true }, requestOptions),
        api.time.projects.getByWorkspace(wsId, requestOptions),
        itemData.request_type_id
          ? api.requestTypes.getFields(itemData.request_type_id, requestOptions).catch((error) => {
              if (isAbortError(error)) throw error;
              return [];
            })
          : Promise.resolve([]),
      ]);
      if (token !== this.#loadToken) return;

      this.workspace = workspaceData;
      this.customFieldDefinitions = customFieldsData?.data || [];

      // Filter milestones by workspace restrictions
      let allMilestones = milestonesData || [];
      if (this.workspace?.milestone_categories?.length > 0) {
        const allowedCategoryIds = this.workspace.milestone_categories;
        this.milestones = allMilestones.filter((m) => allowedCategoryIds.includes(m.category_id));
      } else {
        this.milestones = allMilestones;
      }

      this.iterations = iterationsData || [];
      this.timeProjects = projectsData || [];
      this.requestTypeFields = requestTypeFieldsData || [];

      this.linkTypes = linkTypesData;
      this.#applyLinks(linksData);

      // Sync editing state from item before the secondary loaders complete so
      // the core detail surface can render as soon as the critical data lands.
      this.#syncEditingFromItem();

      // Resolve the workspace's configuration set once. Priorities and screen-
      // field resolution both need it; fetching it twice was a redundant round
      // trip on every item open.
      const configSet = this.workspace?.configuration_set_id
        ? await api.configurationSets
            .get(this.workspace.configuration_set_id, requestOptions)
            .catch((err) => {
              if (isAbortError(err)) throw err;
              console.warn('Failed to load configuration set:', err);
              return null;
            })
        : null;
      if (token !== this.#loadToken) return;

      // These resources are independent once the item, workspace, and config
      // set are known. Running them together removes the former priorities →
      // item-types → children → screens → diagrams → actions waterfall.
      await Promise.all([
        this.#loadPriorities(configSet, requestOptions),
        this.#loadAvailableStatusTransitions(requestOptions),
        this.#loadWatchStatus(requestOptions),
        this.#loadItemTypeData(requestOptions),
        this.loadChildItems(requestOptions),
        this.#loadWorkspaceScreenFields(configSet, requestOptions),
        this.loadDiagrams(requestOptions),
        this.#loadManualActions(requestOptions),
      ]);
      if (token !== this.#loadToken) return;

      // Parent enrichment reuses the item types loaded in the parallel phase.
      if (this.item.parent_id) {
        await this.#loadParentHierarchy(requestOptions);
      } else {
        this.parentHierarchy = [];
      }
      return this.item;
    } catch (err) {
      if (token !== this.#loadToken || isAbortError(err)) return;
      console.error('Failed to load item or workspace:', err);
      this.error = err.message || 'Failed to load data';
      this.item = null;
    } finally {
      if (token === this.#loadToken) {
        if (this.#loadController === controller) this.#loadController = null;
        this.loading = false;
        this.loadingLinks = false;
        this.transitioning = false;
      }
    }
  }

  /**
   * Lightweight background refresh for an already-open item detail view.
   * Unlike loadItem(), this only fetches the item record and merges it into
   * current state so agent-driven status/comment-adjacent updates appear
   * without clobbering any field the local user is actively editing.
   */
  async refreshCurrentItem() {
    if (!this.itemId || this.loading || this.saving) return;

    try {
      const nextItem = await api.items.get(this.itemId);
      if (!nextItem || String(nextItem.id) !== String(this.itemId)) return;

      const previousStatusID = this.item?.status_id;
      const previousItemTypeID = this.item?.item_type_id;
      const previousParentID = this.item?.parent_id;

      this.item = this.#mergeItemPreservingActiveEdits(this.item, nextItem);
      this.#syncInactiveEditingFromItem();

      if (previousStatusID !== this.item.status_id) {
        await this.#loadAvailableStatusTransitions();
      }
      if (previousItemTypeID !== this.item.item_type_id) {
        await this.#loadItemTypeData();
        await this.#loadWorkspaceScreenFields();
      }
      if (previousParentID !== this.item.parent_id) {
        if (this.item.parent_id) {
          await this.#loadParentHierarchy();
        } else {
          this.parentHierarchy = [];
        }
      }
    } catch (err) {
      // A 404 means the item was deleted out from under us. Surface it so the
      // view can close instead of silently keeping stale data (WI-484).
      if (err?.status === 404) {
        this.markDeleted();
        return;
      }
      console.warn('Failed to refresh item detail:', err);
    }
  }

  /**
   * Mark the open item as gone (deleted elsewhere). Idempotent; the detail view
   * reacts to `notFound` by closing/navigating away.
   */
  markDeleted() {
    this.notFound = true;
  }

  /**
   * Load worklogs only when the Time tab is opened. Calls are single-flighted
   * per item so the route effect and a fast tab click cannot duplicate work.
   */
  async loadWorklogs({ force = false } = {}) {
    if (!this.itemId) return;
    const itemId = this.itemId;
    if (!force && this.#worklogsLoadedItemId === itemId) return this.timeWorklogs;
    if (!force && this.#worklogsPromise && this.#worklogsPromiseItemId === itemId) {
      return this.#worklogsPromise;
    }

    this.#worklogsController?.abort();
    const controller = new AbortController();
    this.#worklogsController = controller;
    this.#worklogsPromiseItemId = itemId;
    this.timeWorklogsLoading = true;

    const promise = api.time.worklogs
      .getByItem(itemId, { signal: controller.signal })
      .then((worklogs) => {
        if (this.itemId === itemId) {
          this.timeWorklogs = worklogs || [];
          this.#worklogsLoadedItemId = itemId;
        }
        return this.timeWorklogs;
      })
      .catch((err) => {
        if (isAbortError(err)) return this.timeWorklogs;
        console.error('Failed to load worklogs:', err);
        return this.timeWorklogs;
      })
      .finally(() => {
        if (this.#worklogsController === controller) {
          this.#worklogsController = null;
          this.#worklogsPromise = null;
          this.#worklogsPromiseItemId = null;
          this.timeWorklogsLoading = false;
        }
      });
    this.#worklogsPromise = promise;
    return promise;
  }

  /**
   * Load picker data used only by TimeLogModal. These broad global lists no
   * longer block every item open and are shared after their first use.
   */
  async loadTimeModalData() {
    if (this.#timeModalDataLoaded) return;
    if (this.#timeModalDataPromise) return this.#timeModalDataPromise;

    const controller = new AbortController();
    this.#timeModalDataController = controller;
    this.timeModalDataLoading = true;
    const requestOptions = { signal: controller.signal };
    const fallback = (promise, label) =>
      promise.catch((err) => {
        if (isAbortError(err)) throw err;
        console.warn(`Failed to load ${label} for time logging:`, err);
        return [];
      });

    const promise = Promise.all([
      fallback(api.customerOrganisations.getAll({}, requestOptions), 'customers'),
      fallback(api.items.getAll({ limit: 100 }, requestOptions), 'work items'),
      fallback(api.workspaces.getAll({}, requestOptions), 'workspaces'),
    ])
      .then(([customers, workItems, workspaces]) => {
        this.customers = customers || [];
        this.workItems = workItems?.items || workItems || [];
        this.workspaces = workspaces || [];
        this.#timeModalDataLoaded = true;
      })
      .catch((err) => {
        if (!isAbortError(err)) console.error('Failed to load time-log modal data:', err);
      })
      .finally(() => {
        if (this.#timeModalDataController === controller) {
          this.#timeModalDataController = null;
          this.#timeModalDataPromise = null;
          this.timeModalDataLoading = false;
        }
      });
    this.#timeModalDataPromise = promise;
    return promise;
  }

  /** Reload worklogs after a timer or manual time-entry mutation. */
  async reloadWorklogs() {
    await this.loadWorklogs({ force: true });
    if (this.includeChildItems) this.loadTimeRollup({ force: true });
  }

  /**
   * Fetch the rolled-up estimate / logged minutes across the current item
   * and its descendants. Cached; pass { force: true } to refetch.
   */
  async loadTimeRollup({ force = false } = {}) {
    if (!this.itemId) return;
    if (this.timeRollup && !force) return;
    this.timeRollupLoading = true;
    try {
      this.timeRollup = await api.items.getTimeRollup(this.itemId);
    } catch (err) {
      console.error('Failed to load time rollup:', err);
      this.timeRollup = null;
    } finally {
      this.timeRollupLoading = false;
    }
  }

  /**
   * Load child items.
   */
  async loadChildItems(requestOptions = {}) {
    if (!this.itemId) return;
    try {
      this.loadingChildItems = true;
      const response = await api.items.getChildren(this.itemId, requestOptions);
      let nextChildItems = [];
      if (Array.isArray(response)) {
        nextChildItems = response;
      } else if (response?.items) {
        nextChildItems = response.items;
      } else if (response?.data) {
        nextChildItems = response.data;
      }

      if (!childItemListsMatch(this.childItems, nextChildItems)) {
        this.childItems = nextChildItems;
      }
    } catch (err) {
      if (isAbortError(err)) return;
      console.error('Failed to load child items:', err);
      this.childItems = [];
    } finally {
      if (!requestOptions.signal?.aborted) this.loadingChildItems = false;
    }
  }

  /**
   * Refresh only the generic item links. Used by link mutations and SSE link
   * events so they do not restart the entire item-detail bootstrap.
   */
  async loadLinks() {
    if (!this.itemId) return;
    this.#linksController?.abort();
    const controller = new AbortController();
    this.#linksController = controller;
    const itemId = this.itemId;
    try {
      this.loadingLinks = true;
      const links = await api.links.getForItem('items', itemId, {
        signal: controller.signal,
      });
      if (this.itemId !== itemId) return;
      this.#applyLinks(links);
    } catch (err) {
      if (isAbortError(err)) return;
      console.error('Failed to load item links:', err);
    } finally {
      if (this.#linksController === controller) {
        this.#linksController = null;
        this.loadingLinks = false;
      }
    }
  }

  /**
   * Load diagrams for the item.
   */
  async loadDiagrams(requestOptions = {}) {
    if (!this.item?.id) return;
    try {
      this.loadingDiagrams = true;
      this.diagrams = (await api.getDiagrams(this.item.id, requestOptions)) || [];
    } catch (err) {
      if (isAbortError(err)) return;
      console.error('Failed to load diagrams:', err);
      this.diagrams = [];
    } finally {
      if (!requestOptions.signal?.aborted) this.loadingDiagrams = false;
    }
  }

  // === Private Data Loading Methods ===

  #applyLinks(linksData) {
    const links = [];
    if (linksData?.outgoing) links.push(...linksData.outgoing);
    if (linksData?.incoming) links.push(...linksData.incoming);
    this.itemLinks = links;
  }

  /**
   * @param {object|null} [configSet] Pre-resolved configuration set from the
   *   caller (loadItem resolves it once and shares it). Pass `undefined` to let
   *   this method fetch it itself; pass `null` to signal "configured but the
   *   fetch failed" (yields no priorities, matching the old error path).
   */
  async #loadPriorities(configSet = undefined, requestOptions = {}) {
    if (!this.workspace) return;
    try {
      if (this.workspace.configuration_set_id) {
        const cs =
          configSet !== undefined
            ? configSet
            : await api.configurationSets.get(this.workspace.configuration_set_id, requestOptions);
        this.priorities = cs?.priorities_detailed || [];
      } else {
        this.priorities = await api.priorities.getAll({}, requestOptions);
      }
      this.priorities = this.priorities.sort((a, b) => a.sort_order - b.sort_order);
    } catch (err) {
      if (isAbortError(err)) return;
      console.error('Failed to load priorities:', err);
      this.priorities = [];
    }
  }

  async #loadAvailableStatusTransitions(requestOptions = {}) {
    if (!this.item?.id) return;
    const itemId = this.item.id;
    try {
      this.loadingStatusTransitions = true;
      const result = await api.items.getAvailableStatusTransitions(itemId, requestOptions);
      if (this.item?.id !== itemId) return;
      this.availableStatusTransitions = result.available_transitions || [];
      this.pendingApproval = result.pending_approval || null;
    } catch (err) {
      if (isAbortError(err)) return;
      console.error('Failed to load status transitions:', err);
      this.availableStatusTransitions = [];
      this.pendingApproval = null;
    } finally {
      if (!requestOptions.signal?.aborted && this.item?.id === itemId) {
        this.loadingStatusTransitions = false;
      }
    }
  }

  /**
   * Re-fetch available transitions (and pending-approval state). Call after a
   * decision finalizes so the picker reflects the new gating.
   */
  async refreshAvailableTransitions() {
    await this.#loadAvailableStatusTransitions();
  }

  async #loadWatchStatus(requestOptions = {}) {
    if (!this.item?.id) return;
    const itemId = this.item.id;
    try {
      this.loadingWatchStatus = true;
      const result = await api.items.getWatchStatus(itemId, requestOptions);
      if (this.item?.id !== itemId) return;
      this.isWatching = result.watching || false;
    } catch (err) {
      if (isAbortError(err)) return;
      console.error('Failed to load watch status:', err);
      this.isWatching = false;
    } finally {
      if (!requestOptions.signal?.aborted && this.item?.id === itemId) {
        this.loadingWatchStatus = false;
      }
    }
  }

  async #loadParentHierarchy(requestOptions = {}) {
    try {
      const ancestors = await api.items.getAncestors(this.item.id, requestOptions);
      try {
        // Reuse the already-loaded type list (set by #loadItemTypeData, which
        // runs first); only fetch as a fallback if it isn't populated yet.
        const itemTypesData = this.itemTypes?.length
          ? this.itemTypes
          : await api.itemTypes.getAll({}, requestOptions);
        this.parentHierarchy = ancestors.map((ancestor) => {
          if (ancestor.item_type_id) {
            const itemType = itemTypesData.find((type) => type.id === ancestor.item_type_id);
            return { ...ancestor, itemType };
          }
          return ancestor;
        });
      } catch (err) {
        if (isAbortError(err)) throw err;
        console.warn('Failed to load item types for parent hierarchy:', err);
        this.parentHierarchy = ancestors;
      }
    } catch (err) {
      if (isAbortError(err)) return;
      console.error('Failed to load ancestors:', err);
      this.parentHierarchy = [];
    }
  }

  async #loadItemTypeData(requestOptions = {}) {
    try {
      const [itemTypesData, hierarchyLevels] = await Promise.all([
        api.itemTypes.getAll({}, requestOptions),
        api.hierarchyLevels.getAll({}, requestOptions),
      ]);

      this.itemTypes = itemTypesData || [];

      if (this.item.item_type_id) {
        this.currentItemType = this.itemTypes.find((type) => type.id === this.item.item_type_id);
        if (this.currentItemType) {
          this.currentHierarchyLevel = hierarchyLevels.find(
            (level) => level.level === this.currentItemType.hierarchy_level
          );
        }
      }

      // Find available sub-issue types (next level down)
      if (this.currentItemType && this.currentHierarchyLevel) {
        const nextLevel = this.currentHierarchyLevel.level + 1;
        this.availableSubIssueTypes = this.itemTypes.filter(
          (type) => type.hierarchy_level === nextLevel
        );
      } else {
        this.availableSubIssueTypes = [];
      }
    } catch (err) {
      if (isAbortError(err)) return;
      console.error('Failed to load item type data:', err);
      this.currentItemType = null;
      this.currentHierarchyLevel = null;
      this.availableSubIssueTypes = [];
    }
  }

  /**
   * @param {object|null} [configSet] Pre-resolved configuration set shared by
   *   loadItem. Pass `undefined` to let this method fetch it itself (the
   *   refresh path); `null` means "none configured / fetch failed".
   */
  async #loadWorkspaceScreenFields(configSet = undefined, requestOptions = {}) {
    try {
      let editScreenId = null;
      let viewScreenId = null;

      let cs = configSet;
      if (cs === undefined) {
        cs = this.workspace?.configuration_set_id
          ? await api.configurationSets.get(this.workspace.configuration_set_id, requestOptions)
          : null;
      }
      if (cs) {
        const itemTypeId = this.item?.item_type_id;
        const screenIds = resolveEffectiveScreenIds(cs, itemTypeId, 1);
        editScreenId = screenIds.edit;
        viewScreenId = screenIds.view;
      }

      // Hardcoded fallback (preserves legacy behavior when nothing is
      // configured). resolveEffectiveScreenIds already chains through create as
      // the universal fallback, so a null here means truly nothing is set.
      if (!editScreenId) editScreenId = 1;

      // If view screen is missing or matches edit, only fetch one — same
      // behavior as before (every visible field is editable).
      const sameScreen = !viewScreenId || viewScreenId === editScreenId;
      const [editScreen, viewScreen] = await Promise.all([
        api.screens.get(editScreenId, requestOptions),
        sameScreen ? Promise.resolve(null) : api.screens.get(viewScreenId, requestOptions),
      ]);

      const fieldConfig = buildDetailScreenFieldConfig(editScreen, sameScreen ? null : viewScreen);
      this.workspaceScreenFields = fieldConfig.visibleCustomFields;
      this.workspaceScreenSystemFields = fieldConfig.visibleSystemFields;
      this.editableScreenFieldIds = fieldConfig.editableCustomFieldIds;
      this.editableScreenSystemFields = fieldConfig.editableSystemFields;
    } catch (err) {
      if (isAbortError(err)) return;
      console.error('Failed to load workspace screen fields:', err);
      this.workspaceScreenFields = [];
      this.workspaceScreenSystemFields = [];
      this.editableScreenFieldIds = null;
      this.editableScreenSystemFields = null;
    }
  }

  async #loadManualActions(requestOptions = {}) {
    if (!this.workspaceId) return;
    try {
      const allActions = await api.actions.getAll(this.workspaceId, requestOptions);
      this.manualActions = (allActions || []).filter(
        (a) => a.trigger_type === 'manual' && a.is_enabled
      );
    } catch (err) {
      if (isAbortError(err)) return;
      console.error('Failed to load manual actions:', err);
      this.manualActions = [];
    }
  }

  // === Editing Methods ===

  /**
   * Start editing a field.
   */
  startEditing(field) {
    if (field.startsWith('custom_field_')) {
      const fieldId = field.replace('custom_field_', '');
      this.editing.customFields.active[fieldId] = true;
      const currentValue = this.item.custom_field_values?.[fieldId];
      this.editing.customFields.values[fieldId] =
        currentValue !== null && currentValue !== undefined ? currentValue : '';
      // Trigger reactivity
      this.editing = { ...this.editing };
    } else {
      // Sync value from item before activating edit mode
      this.#syncFieldFromItem(field);
      this.editing[field].active = true;
      // Trigger reactivity
      this.editing = { ...this.editing };
    }
  }

  /**
   * Cancel editing a field.
   */
  cancelEditing(field) {
    if (field.startsWith('custom_field_')) {
      const fieldId = field.replace('custom_field_', '');
      delete this.editing.customFields.active[fieldId];
      delete this.editing.customFields.values[fieldId];
      this.editing = { ...this.editing };
    } else if (this.editing[field]) {
      this.editing[field].active = false;
      this.#syncFieldFromItem(field);
      // Trigger reactivity
      this.editing = { ...this.editing };
    }
  }

  /**
   * Save a field value.
   */
  async saveField(field, directValue = null, assigneeName = null, iterationName = null) {
    if (this.saving) return;

    try {
      this.saving = true;
      let updateData = {};

      if (field === 'title') {
        const newTitle = directValue || this.editing.title.value.trim();
        if (newTitle === this.item.title) {
          this.cancelEditing('title');
          return;
        }
        updateData.title = newTitle;
      } else if (field === 'description') {
        const newDescription = directValue !== null ? directValue : this.editing.description.value;
        if (newDescription === (this.item.description || '')) {
          this.cancelEditing('description');
          return;
        }
        updateData.description = newDescription;
      } else if (field === 'status_id') {
        const newStatusId = directValue !== null ? directValue : null;
        if (newStatusId === this.item.status_id) {
          this.cancelEditing('status');
          return;
        }
        // Status changes must go through the transition endpoint so workflow
        // rules (validators, conditions) are enforced. The item update endpoint
        // rejects status_id.
        const updatedItem = await api.items.transition(this.item.id, newStatusId);
        this.item = { ...this.item, ...updatedItem };
        this.hasChanges = true;
        this.cancelEditing('status');
        return;
      } else if (field === 'priority_id') {
        const newPriorityId = directValue !== null ? directValue : null;
        if (newPriorityId === this.item.priority_id) {
          this.cancelEditing('priority');
          return;
        }
        updateData.priority_id = newPriorityId;
        this.item = { ...this.item, priority_id: newPriorityId };
      } else if (field === 'due_date') {
        const newDueDate = directValue !== null ? directValue : null;
        if (newDueDate === this.item.due_date) {
          this.cancelEditing('dueDate');
          return;
        }
        updateData.due_date = newDueDate;
        this.item = { ...this.item, due_date: newDueDate };
      } else if (field === 'start_date') {
        const newStartDate = directValue !== null ? directValue : null;
        if (newStartDate === this.item.start_date) {
          this.cancelEditing('startDate');
          return;
        }
        updateData.start_date = newStartDate;
        this.item = { ...this.item, start_date: newStartDate };
      } else if (field === 'end_date') {
        const newEndDate = directValue !== null ? directValue : null;
        if (newEndDate === this.item.end_date) {
          this.cancelEditing('endDate');
          return;
        }
        updateData.end_date = newEndDate;
        this.item = { ...this.item, end_date: newEndDate };
      } else if (field === 'milestone') {
        // value is now an array of milestone IDs (multi-milestone). Treat
        // missing/non-array as empty set.
        const newIds = Array.isArray(directValue) ? [...directValue].sort((a, b) => a - b) : [];
        const currentIds = (this.item.milestones || []).map((m) => m.id).sort((a, b) => a - b);
        const sameSet =
          newIds.length === currentIds.length && newIds.every((id, i) => id === currentIds[i]);
        if (sameSet) {
          this.cancelEditing('milestone');
          return;
        }
        updateData.milestone_ids = newIds;
        // Optimistic local update: rebuild milestones array from the picker's
        // current cache so the UI reflects the new selection immediately.
        const nextMilestones = newIds
          .map((id) => this.milestones.find((m) => m.id === id))
          .filter(Boolean);
        this.item = { ...this.item, milestones: nextMilestones };
      } else if (field === 'story_points') {
        const newPoints = directValue !== undefined ? directValue : null;
        if (newPoints === (this.item.story_points ?? null)) {
          return;
        }
        updateData.story_points = newPoints;
        this.item = { ...this.item, story_points: newPoints };
      } else if (field === 'estimate_minutes' || field === 'estimate') {
        const newEstimate = directValue !== undefined ? directValue : null;
        if (newEstimate === (this.item.estimate_minutes ?? null)) {
          return;
        }
        updateData.estimate_minutes = newEstimate;
        this.item = { ...this.item, estimate_minutes: newEstimate };
      } else if (field === 'iteration') {
        const newIteration = directValue !== null ? directValue : null;
        if (newIteration === this.item.iteration_id) {
          return;
        }
        updateData.iteration_id = newIteration;
        this.item = {
          ...this.item,
          iteration_id: newIteration,
          iteration_name: iterationName !== undefined ? iterationName : this.item.iteration_name,
        };
      } else if (field === 'project') {
        const newProject = directValue !== null ? directValue : this.editing.project.value;
        if (typeof newProject === 'object' && newProject !== null) {
          updateData.project_id = newProject.project_id;
          updateData.inherit_project = newProject.inherit_project;
          this.item = {
            ...this.item,
            project_id: newProject.project_id,
            inherit_project: newProject.inherit_project,
          };
        } else {
          if (newProject === this.item.project_id) {
            this.cancelEditing('project');
            return;
          }
          updateData.project_id = newProject;
          this.item = { ...this.item, project_id: newProject };
        }
      } else if (field === 'assignee') {
        const newAssignee = directValue !== undefined ? directValue : this.editing.assignee.value;
        if (newAssignee === this.item.assignee_id) {
          this.cancelEditing('assignee');
          return;
        }
        updateData.assignee_id = newAssignee;
        this.item = {
          ...this.item,
          assignee_id: newAssignee,
          assignee_name: assigneeName !== undefined ? assigneeName : this.item.assignee_name,
        };
      } else if (field.startsWith('custom_field_')) {
        const fieldId = field.replace('custom_field_', '');
        let newValue =
          directValue !== null ? directValue : this.editing.customFields.values[fieldId];
        const currentValue = this.item.custom_field_values?.[fieldId] || '';

        // Convert number fields
        const fieldDef = this.customFieldDefinitions.find((f) => f.id === parseInt(fieldId, 10));
        if (
          fieldDef?.field_type === 'number' &&
          newValue !== null &&
          newValue !== undefined &&
          newValue !== ''
        ) {
          newValue = parseFloat(newValue);
          if (Number.isNaN(newValue)) {
            newValue =
              directValue !== null ? directValue : this.editing.customFields.values[fieldId];
          }
        }

        if (newValue === currentValue) {
          this.cancelEditing(field);
          return;
        }

        updateData.custom_field_values = {
          ...(this.item.custom_field_values || {}),
          [fieldId]: newValue,
        };
      }

      // Update via API
      const updatedItem = await api.items.update(this.item.id, updateData);
      this.item = { ...this.item, ...updatedItem };

      // Update assignee/iteration names if provided
      if (field === 'assignee' && assigneeName !== null) {
        this.item = { ...this.item, assignee_name: assigneeName };
      }
      if (field === 'iteration' && iterationName !== undefined) {
        this.item = { ...this.item, iteration_name: iterationName };
      }

      this.hasChanges = true;
      this.cancelEditing(field);
    } catch (err) {
      console.error('Failed to update item:', err);
      throw err;
    } finally {
      this.saving = false;
    }
  }

  // === Auto-sync editing values when item loads/changes ===

  #syncEditingFromItem() {
    if (!this.item) return;
    for (const [editKey, itemKey] of Object.entries(FIELD_MAP)) {
      if (editKey === 'milestone') {
        // milestones is an array of objects on the item; the editing value
        // tracks the array of IDs the picker binds to.
        this.editing[editKey].value = (this.item.milestones || []).map((m) => m.id);
        continue;
      }
      this.editing[editKey].value = STRING_FIELDS.has(editKey)
        ? this.item[itemKey] || ''
        : this.item[itemKey];
    }
    this.editing.customFields.values = { ...(this.item.custom_field_values || {}) };
  }

  #syncFieldFromItem(field) {
    if (!this.item) return;
    if (FIELD_MAP[field] && this.editing[field]) {
      if (field === 'milestone') {
        this.editing[field].value = (this.item.milestones || []).map((m) => m.id);
        return;
      }
      this.editing[field].value = this.item[FIELD_MAP[field]];
    }
  }

  #syncInactiveEditingFromItem() {
    if (!this.item) return;
    for (const [editKey, itemKey] of Object.entries(FIELD_MAP)) {
      if (this.editing[editKey]?.active) continue;
      if (editKey === 'milestone') {
        this.editing[editKey].value = (this.item.milestones || []).map((m) => m.id);
        continue;
      }
      this.editing[editKey].value = STRING_FIELDS.has(editKey)
        ? this.item[itemKey] || ''
        : this.item[itemKey];
    }

    const nextCustomValues = { ...(this.item.custom_field_values || {}) };
    for (const fieldId of Object.keys(this.editing.customFields.active || {})) {
      if (this.editing.customFields.active[fieldId]) {
        nextCustomValues[fieldId] = this.editing.customFields.values[fieldId];
      }
    }
    this.editing.customFields.values = nextCustomValues;
    this.editing = { ...this.editing };
  }

  #mergeItemPreservingActiveEdits(current, next) {
    if (!current) return next;
    const merged = { ...current, ...next };

    for (const [editKey, itemKey] of Object.entries(FIELD_MAP)) {
      if (!this.editing[editKey]?.active) continue;

      if (editKey === 'milestone') {
        merged.milestones = current.milestones;
        continue;
      }

      merged[itemKey] = current[itemKey];
      for (const relatedKey of RELATED_ITEM_FIELDS[editKey] || []) {
        if (relatedKey in current) merged[relatedKey] = current[relatedKey];
      }
    }

    const activeCustomFields = Object.keys(this.editing.customFields.active || {}).filter(
      (fieldId) => this.editing.customFields.active[fieldId]
    );
    if (activeCustomFields.length > 0) {
      merged.custom_field_values = { ...(next.custom_field_values || {}) };
      for (const fieldId of activeCustomFields) {
        if (current.custom_field_values && fieldId in current.custom_field_values) {
          merged.custom_field_values[fieldId] = current.custom_field_values[fieldId];
        }
      }
    }

    return merged;
  }

  // === Watch Actions ===

  async toggleWatch() {
    if (!this.item?.id) return;
    try {
      if (this.isWatching) {
        await api.items.removeWatch(this.item.id);
        this.isWatching = false;
      } else {
        await api.items.addWatch(this.item.id);
        this.isWatching = true;
      }
      this.hasChanges = true;
    } catch (err) {
      console.error('Failed to toggle watch:', err);
      throw err;
    }
  }

  // === Link Actions ===

  async createLink(linkTypeId, targetId, targetType = 'item') {
    try {
      await api.links.create({
        source_type: 'item',
        source_id: parseInt(this.itemId, 10),
        target_type: targetType,
        target_id: parseInt(targetId, 10),
        link_type_id: parseInt(linkTypeId, 10),
      });
      await this.loadLinks();
    } catch (err) {
      console.error('Error creating link:', err);
      throw err;
    }
  }

  async removeLink(linkId) {
    try {
      await api.links.delete(linkId);
      await this.loadLinks();
    } catch (err) {
      console.error('Error removing link:', err);
      throw err;
    }
  }

  // === Copy Item ===

  async copyItem() {
    try {
      const copiedItem = await api.items.copy(this.item.id);
      return copiedItem;
    } catch (err) {
      console.error('Failed to copy item:', err);
      throw err;
    }
  }

  // === Execute Action ===

  async executeAction(actionId) {
    try {
      await api.actions.execute(this.workspaceId, actionId, this.item.id);
    } catch (err) {
      console.error('Failed to execute action:', err);
      throw err;
    }
  }

  // === Modal Controls ===

  openDeleteDialog() {
    this.showDeleteDialog = true;
  }

  closeDeleteDialog() {
    this.showDeleteDialog = false;
  }

  openLinkModal(preselectLinkTypeId = null) {
    this.linkModalPreselectTypeId = preselectLinkTypeId;
    this.showLinkModal = true;
  }

  closeLinkModal() {
    this.showLinkModal = false;
    this.linkModalPreselectTypeId = null;
  }

  openTestCaseModal(testCaseId) {
    this.selectedTestCaseId = testCaseId;
    this.showTestCaseModal = true;
  }

  closeTestCaseModal() {
    this.showTestCaseModal = false;
    this.selectedTestCaseId = null;
  }

  openTimeLogModal(worklog = null) {
    this.editingWorklog = worklog;
    this.showTimeLogModal = true;
  }

  closeTimeLogModal() {
    this.showTimeLogModal = false;
    this.editingWorklog = null;
  }

  // === Get Default Project for Time Logging ===

  getDefaultProjectForTimeLogging() {
    if (this.item?.time_project_id) return this.item.time_project_id;
    if (this.item?.effective_project_id) return this.item.effective_project_id;
    if (this.workspace?.time_project_id) return this.workspace.time_project_id;
    return null;
  }

  // === Reset ===

  /**
   * Full reset.
   */
  reset() {
    this.#loadToken += 1;
    this.#loadController?.abort();
    this.#linksController?.abort();
    this.#worklogsController?.abort();
    this.#timeModalDataController?.abort();
    this.#loadController = null;
    this.#linksController = null;
    this.#worklogsController = null;
    this.#worklogsPromise = null;
    this.#worklogsPromiseItemId = null;
    this.#worklogsLoadedItemId = null;
    this.#timeModalDataController = null;
    this.#timeModalDataPromise = null;
    this.#timeModalDataLoaded = false;
    this.item = null;
    this.itemId = null;
    this.workspaceId = null;
    this.loading = true;
    this.error = null;
    this.saving = false;
    this.notFound = false;
    this.workspace = null;

    this.editing = { ...DEFAULT_EDITING_STATE };

    this.parentHierarchy = [];
    this.childItems = [];
    this.loadingChildItems = false;
    this.milestones = [];
    this.iterations = [];
    this.priorities = [];
    this.itemTypes = [];
    this.currentItemType = null;
    this.currentHierarchyLevel = null;
    this.availableSubIssueTypes = [];
    this.customFieldDefinitions = [];
    this.workspaceScreenFields = [];
    this.workspaceScreenSystemFields = [];
    this.requestTypeFields = [];
    this.editableScreenFieldIds = null;
    this.editableScreenSystemFields = null;
    this.availableStatusTransitions = [];
    this.loadingStatusTransitions = false;
    this.pendingApproval = null;
    this.itemLinks = [];
    this.linkTypes = [];
    this.loadingLinks = false;
    this.isWatching = false;
    this.loadingWatchStatus = false;
    this.timeProjects = [];
    this.timeWorklogs = [];
    this.timeWorklogsLoading = false;
    this.timeModalDataLoading = false;
    this.includeChildItems = false;
    this.timeRollup = null;
    this.timeRollupLoading = false;
    this.customers = [];
    this.workItems = [];
    this.workspaces = [];
    this.diagrams = [];
    this.loadingDiagrams = false;
    this.manualActions = [];
    this.showDeleteDialog = false;
    this.showLinkModal = false;
    this.linkModalPreselectTypeId = null;
    this.showTestCaseModal = false;
    this.selectedTestCaseId = null;
    this.showTimeLogModal = false;
    this.editingWorklog = null;
    this.hasChanges = false;
    this.transitioning = false;
    this.dropdownItems = [];
  }
}

export const itemDetailStore = new ItemDetailStore();
