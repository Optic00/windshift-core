/**
 * Store for managing Work Item Form state.
 * Uses Svelte 5 class-based reactive state pattern.
 * Centralizes form data, validation, data loading, and selection persistence.
 */
import { api } from '../api.js';
import { resolveScreenId } from '../utils/screenResolution.js';
import { getSystemFieldName } from './fieldConfig.js';

const STORAGE_KEYS = {
  workspace: 'vertex_create_modal_workspace',
  itemType: 'vertex_create_modal_item_type',
};

// System fields that are auto-managed and should not be shown in create form
const EXCLUDED_SYSTEM_FIELDS = ['status'];

class WorkItemFormStore {
  // === Form Data ===
  formData = $state({
    name: '',
    description: '',
    due_date: '',
    start_date: '',
    end_date: '',
    workspace_id: null,
    priority_id: null,
    milestone_ids: [],
    assignee_id: null,
    item_type_id: null,
  });
  customFieldValues = $state({});
  validationErrors = $state([]);
  pendingDescriptionImages = $state([]);

  // === Selection Context ===
  selectedWorkspace = $state(null);
  parentItem = $state(null);
  restrictedItemTypes = $state(null);

  // === Data Loading State ===
  users = $state([]);
  usersLoaded = $state(false);

  allMilestones = $state([]);
  milestones = $state([]);
  milestonesLoading = $state(false);
  milestonesLoaded = $state(false);
  milestonesLoadedForKey = $state(null);

  itemTypes = $state([]);
  hierarchyLevels = $state([]);
  availableItemTypes = $state([]);
  itemTypesLoaded = $state(false);

  // === Work item templates (WI-438) ===
  // templateOptions: selectable templates valid for the current type (type-
  // targeted + global). mandatoryTemplate: the active mandatory template the
  // current type enforces (or null) — when set, its body is auto-applied and
  // the picker is locked. templateApplyNonce forces the Milkdown editor to
  // re-mount when a body is written into formData.description (the editable
  // editor does not sync external content changes otherwise).
  templateOptions = $state([]);
  mandatoryTemplate = $state(null);
  selectedTemplateId = $state(null);
  templatesLoading = $state(false);
  templateApplyNonce = $state(0);
  #templatesLoadedForKey = null;

  allCustomFields = $state([]);
  customFields = $state([]);
  customFieldsLoaded = $state(false);

  screenFields = $state([]);
  screenSystemFields = $state([]);
  loadingScreenFields = $state(false);

  workspaceDetails = $state(null);
  currentConfigSet = $state(null);

  // === Cache Keys ===
  configSetLoadedForWorkspace = $state(null);
  screenFieldsLoadedForKey = $state(null);

  // === Persistence State ===
  storedWorkspaceId = $state(null);
  storedItemTypeId = $state(null);
  lastPersistedWorkspaceId = $state(null);
  lastPersistedItemTypeId = $state(null);
  storedItemTypeApplied = $state(false);
  configSetDefaultApplied = $state(false);

  // === Initialization Flag ===
  #initialized = false;
  #milestonesLoadToken = 0;

  // === Derived Values (getters) ===

  /**
   * Get the currently selected item type object.
   */
  get selectedItemType() {
    return this.availableItemTypes.find((t) => t.id === this.formData.item_type_id) || null;
  }

  /**
   * Whether the current item type enforces a mandatory template (so the
   * create-modal template picker is disabled and its body auto-applied).
   */
  get templateLocked() {
    return !!this.mandatoryTemplate;
  }

  /**
   * Get priorities from the loaded config set.
   */
  get configSetPriorities() {
    return this.currentConfigSet?.priorities_detailed?.length > 0
      ? this.currentConfigSet.priorities_detailed
      : null;
  }

  /**
   * Get the currently selected assignee object.
   */
  get selectedAssignee() {
    return this.users.find((u) => u.id === this.formData.assignee_id) || null;
  }

  /**
   * Get the currently selected milestone objects (multi-select).
   */
  get selectedMilestones() {
    const ids = this.formData.milestone_ids || [];
    return ids.map((id) => this.milestones.find((m) => m.id === id)).filter(Boolean);
  }

  /**
   * Get non-required custom fields for the overflow menu.
   */
  get nonRequiredCustomFields() {
    return this.customFields.filter((cf) => {
      const screenField = this.screenFields.find(
        (f) => f.field_type === 'custom' && parseInt(f.field_identifier, 10) === cf.id
      );
      return !screenField?.is_required;
    });
  }

  /**
   * Get required system fields that should be shown as full inputs.
   */
  get requiredSystemFields() {
    return this.screenFields.filter(
      (f) =>
        f.is_required &&
        f.field_type === 'system' &&
        !EXCLUDED_SYSTEM_FIELDS.includes(f.field_identifier)
    );
  }

  /**
   * Get required custom fields that should be shown as full inputs.
   */
  get requiredCustomFields() {
    return this.customFields.filter((cf) => {
      const screenField = this.screenFields.find(
        (f) => f.field_type === 'custom' && parseInt(f.field_identifier, 10) === cf.id
      );
      return screenField?.is_required === true;
    });
  }

  // === Data Loading Methods ===

  /**
   * Load assignable users. When workspaceId is provided, fetches only active users
   * via the assignable-users endpoint; otherwise falls back to the general users endpoint.
   */
  async loadUsers(workspaceId = null) {
    if (this.usersLoaded) return;
    try {
      const result = workspaceId ? await api.getAssignableUsers(workspaceId) : await api.getUsers();
      this.users = result || [];
      this.usersLoaded = true;
    } catch (error) {
      console.error('Failed to load users:', error);
      this.users = [];
      this.usersLoaded = true;
    }
  }

  /**
   * Load all custom fields.
   */
  async loadCustomFields() {
    if (this.customFieldsLoaded) return;
    try {
      const result = await api.customFields.getAll();
      this.allCustomFields = result?.data || [];
      this.customFieldsLoaded = true;
    } catch (error) {
      console.error('Failed to load custom fields:', error);
      this.allCustomFields = [];
      this.customFields = [];
      this.customFieldsLoaded = true;
    }
  }

  /**
   * Load all milestones.
   */
  async loadMilestones(workspaceId = null, forceReload = false) {
    const numericWorkspaceId = workspaceId ? Number(workspaceId) : null;
    const loadKey = numericWorkspaceId || 'global';
    if (!forceReload && this.milestonesLoaded && this.milestonesLoadedForKey === loadKey) return;

    const token = ++this.#milestonesLoadToken;
    try {
      this.milestonesLoading = true;
      const filters = numericWorkspaceId
        ? { workspace_id: numericWorkspaceId, include_global: true }
        : {};
      const result = await api.milestones.getAll(filters);
      if (token !== this.#milestonesLoadToken) return;

      this.allMilestones = result || [];
      this.milestonesLoaded = true;
      this.milestonesLoadedForKey = loadKey;
      this.#filterMilestones();
    } catch (error) {
      if (token !== this.#milestonesLoadToken) return;
      console.error('Failed to load milestones:', error);
      this.allMilestones = [];
      this.milestones = [];
      this.milestonesLoaded = true;
      this.milestonesLoadedForKey = loadKey;
    } finally {
      if (token === this.#milestonesLoadToken) {
        this.milestonesLoading = false;
      }
    }
  }

  /**
   * Filter milestones based on workspace categories, while always keeping
   * global milestones available in workspace-scoped forms.
   */
  #filterMilestones() {
    if (!this.workspaceDetails?.milestone_categories?.length) {
      this.milestones = this.allMilestones;
    } else {
      const allowedCategoryIds = this.workspaceDetails.milestone_categories;
      this.milestones = this.allMilestones.filter(
        (m) => m.is_global || allowedCategoryIds.includes(m.category_id)
      );
    }
  }

  /**
   * Load workspace details and filter milestones.
   */
  async loadWorkspaceDetails(workspaceId) {
    if (!workspaceId) {
      this.workspaceDetails = null;
      this.#filterMilestones();
      return;
    }
    try {
      this.workspaceDetails = await api.workspaces.get(workspaceId);
      await this.loadMilestones(workspaceId);
    } catch (error) {
      console.error('Failed to load workspace details:', error);
      this.workspaceDetails = null;
      this.#filterMilestones();
    }
  }

  /**
   * Load all item types and hierarchy levels.
   */
  async loadItemTypes(forceReload = false) {
    if (this.itemTypesLoaded && !forceReload) return;
    try {
      const [itemTypesResult, hierarchyLevelsResult] = await Promise.all([
        api.itemTypes.getAll(),
        api.hierarchyLevels.getAll(),
      ]);
      this.itemTypes = itemTypesResult || [];
      this.hierarchyLevels = hierarchyLevelsResult || [];

      this.#updateAvailableItemTypes();
      this.itemTypesLoaded = true;
    } catch (error) {
      console.error('Failed to load item types:', error);
      this.itemTypes = [];
      this.hierarchyLevels = [];
      this.availableItemTypes = [];
      this.itemTypesLoaded = true;
    }
  }

  /**
   * Update available item types based on restrictions and config set.
   */
  #updateAvailableItemTypes() {
    let baseTypes = this.itemTypes;

    // Apply restricted item types if set (child item creation)
    if (this.restrictedItemTypes?.length > 0) {
      baseTypes = this.restrictedItemTypes;
    }

    // Apply config set item type restrictions
    if (this.currentConfigSet?.item_type_configs?.length > 0) {
      const allowedItemTypeIds = this.currentConfigSet.item_type_configs.map((c) => c.item_type_id);
      baseTypes = baseTypes.filter((t) => allowedItemTypeIds.includes(t.id));
    }

    this.availableItemTypes = baseTypes.sort(
      (a, b) => a.hierarchy_level - b.hierarchy_level || a.sort_order - b.sort_order
    );

    // Auto-select first item type if current is invalid
    if (
      this.availableItemTypes.length > 0 &&
      !this.availableItemTypes.find((t) => t.id === this.formData.item_type_id)
    ) {
      this.formData.item_type_id = this.availableItemTypes[0].id;
    }

    // Load templates for the resolved type so a mandatory template auto-applies
    // before the create modal's editor mounts (WI-438).
    this.loadTemplatesForCurrentType();
  }

  /**
   * Load configuration set for a workspace.
   */
  async loadConfigSetForWorkspace(workspaceId) {
    if (this.configSetLoadedForWorkspace === workspaceId) return;
    try {
      const response = await api.configurationSets.getAll();
      const configSets = response?.configuration_sets || [];
      this.currentConfigSet = null;
      let defaultConfigSet = null;

      for (const configSet of configSets) {
        if (configSet.is_default) defaultConfigSet = configSet;
        if (configSet.workspace_ids?.includes(workspaceId)) {
          this.currentConfigSet = await api.configurationSets.get(configSet.id);
          break;
        }
      }

      if (!this.currentConfigSet && defaultConfigSet) {
        this.currentConfigSet = await api.configurationSets.get(defaultConfigSet.id);
      }

      this.configSetLoadedForWorkspace = workspaceId;
      this.#updateAvailableItemTypes();
    } catch (error) {
      console.error('Failed to load config set:', error);
      this.currentConfigSet = null;
      this.configSetLoadedForWorkspace = workspaceId;
      this.#updateAvailableItemTypes();
    }
  }

  /**
   * Resolve the create screen ID for an item type.
   */
  #resolveCreateScreenId(itemTypeId) {
    return resolveScreenId(this.currentConfigSet, itemTypeId, 'create') ?? 1;
  }

  /**
   * Load screen fields for a specific workspace/item type combination.
   */
  async loadScreenFieldsForItemType(workspaceId, itemTypeId) {
    const key = `${workspaceId}-${itemTypeId}`;
    if (this.loadingScreenFields || this.screenFieldsLoadedForKey === key) return;
    try {
      this.loadingScreenFields = true;
      const createScreenId = this.#resolveCreateScreenId(itemTypeId);
      const fields = await api.screens.getFields(createScreenId);
      this.screenFields = fields || [];

      this.screenSystemFields = this.screenFields
        .filter((field) => field.field_type === 'system')
        .map((field) => field.field_identifier);

      const customFieldIds = this.screenFields
        .filter((field) => field.field_type === 'custom')
        .map((field) => parseInt(field.field_identifier, 10));

      const filteredCustomFields = this.allCustomFields.filter((field) =>
        customFieldIds.includes(field.id)
      );

      // Reset custom field values for new fields
      this.customFieldValues = {};
      filteredCustomFields.forEach((field) => {
        this.customFieldValues[field.id] = '';
      });

      this.customFields = filteredCustomFields;
      this.screenFieldsLoadedForKey = key;
    } catch (error) {
      console.error('Failed to load screen fields:', error);
      this.screenSystemFields = ['priority', 'milestone'];
      this.screenFields = [];
      this.customFields = [];
      this.customFieldValues = {};
      this.screenFieldsLoadedForKey = key;
    } finally {
      this.loadingScreenFields = false;
    }
  }

  // === Field Helpers ===

  /**
   * Check if a system field is required.
   */
  isFieldRequired(fieldIdentifier) {
    const screenField = this.screenFields.find((f) => f.field_identifier === fieldIdentifier);
    return screenField?.is_required === true;
  }

  /**
   * Check if a system field is configured (in screen fields).
   */
  isFieldConfigured(fieldIdentifier) {
    return this.screenSystemFields.includes(fieldIdentifier);
  }

  // === Selection Methods ===

  /**
   * Set the selected workspace.
   */
  setWorkspace(workspace) {
    this.selectedWorkspace = workspace;
    this.formData.workspace_id = workspace?.id || null;

    if (workspace?.id) {
      this.#persistWorkspaceSelection(workspace.id);
      this.loadWorkspaceDetails(workspace.id);
      this.loadConfigSetForWorkspace(workspace.id);
    } else {
      this.workspaceDetails = null;
      this.#filterMilestones();
    }
  }

  /**
   * Set the selected item type.
   */
  setItemType(itemTypeId) {
    this.formData.item_type_id = itemTypeId;
    this.#persistItemTypeSelection(itemTypeId);
    this.loadTemplatesForCurrentType();
  }

  /**
   * Load the work item templates valid for the current (workspace, item type).
   * Populates the selectable picker options and, when the type enforces an
   * active mandatory template, auto-applies its body into the description and
   * locks the picker. No-op until both workspace and item type are known.
   */
  async loadTemplatesForCurrentType() {
    const workspaceId = this.formData.workspace_id;
    const itemTypeId = this.formData.item_type_id;
    if (!workspaceId || !itemTypeId) {
      this.templateOptions = [];
      this.mandatoryTemplate = null;
      this.selectedTemplateId = null;
      this.#templatesLoadedForKey = null;
      return;
    }
    const key = `${workspaceId}:${itemTypeId}`;
    if (this.#templatesLoadedForKey === key) return;
    this.#templatesLoadedForKey = key;

    this.templatesLoading = true;
    try {
      const list =
        (await api.itemTemplates.getAll({ workspace_id: workspaceId, item_type_id: itemTypeId })) ??
        [];
      // Guard against an out-of-order response after another type change.
      if (`${this.formData.workspace_id}:${this.formData.item_type_id}` !== key) return;

      const mandatory = list.find((t) => t.mode === 'mandatory') || null;
      this.templateOptions = list.filter((t) => t.mode === 'selectable');
      this.mandatoryTemplate = mandatory;
      if (mandatory) {
        this.formData.description = mandatory.description_body || '';
        this.selectedTemplateId = mandatory.id;
        this.templateApplyNonce += 1;
      } else {
        this.selectedTemplateId = null;
      }
    } catch (err) {
      console.error('Failed to load item templates:', err);
      this.templateOptions = [];
      this.mandatoryTemplate = null;
      this.#templatesLoadedForKey = null;
    } finally {
      this.templatesLoading = false;
    }
  }

  /**
   * Apply a selectable template's body into the description (from the picker).
   */
  applyTemplate(templateId) {
    const tmpl = this.templateOptions.find((t) => t.id === templateId);
    if (!tmpl) return;
    this.formData.description = tmpl.description_body || '';
    this.selectedTemplateId = templateId;
    this.templateApplyNonce += 1;
  }

  /**
   * Set the parent item context (for child item creation).
   */
  setParentItem(parent, allowedItemTypes = null) {
    this.parentItem = parent;
    this.restrictedItemTypes = allowedItemTypes;
    this.#updateAvailableItemTypes();
  }

  // === Persistence ===

  /**
   * Load stored selections from localStorage.
   */
  loadStoredSelections() {
    if (typeof window === 'undefined') return;
    try {
      const workspaceValue = window.localStorage.getItem(STORAGE_KEYS.workspace);
      if (workspaceValue) {
        const parsedWorkspace = parseInt(workspaceValue, 10);
        this.storedWorkspaceId = Number.isNaN(parsedWorkspace) ? null : parsedWorkspace;
      }
    } catch {
      this.storedWorkspaceId = null;
    }
    try {
      const itemTypeValue = window.localStorage.getItem(STORAGE_KEYS.itemType);
      if (itemTypeValue) {
        const parsedItemType = parseInt(itemTypeValue, 10);
        this.storedItemTypeId = Number.isNaN(parsedItemType) ? null : parsedItemType;
      }
    } catch {
      this.storedItemTypeId = null;
    }
  }

  #persistWorkspaceSelection(workspaceId) {
    if (typeof window === 'undefined' || !workspaceId) return;
    if (workspaceId === this.lastPersistedWorkspaceId) return;
    try {
      window.localStorage.setItem(STORAGE_KEYS.workspace, String(workspaceId));
      this.storedWorkspaceId = workspaceId;
      this.lastPersistedWorkspaceId = workspaceId;
    } catch {
      // Ignore localStorage errors
    }
  }

  #persistItemTypeSelection(itemTypeId) {
    if (typeof window === 'undefined' || !itemTypeId) return;
    if (itemTypeId === this.lastPersistedItemTypeId) return;
    try {
      window.localStorage.setItem(STORAGE_KEYS.itemType, String(itemTypeId));
      this.storedItemTypeId = itemTypeId;
      this.lastPersistedItemTypeId = itemTypeId;
    } catch {
      // Ignore localStorage errors
    }
  }

  /**
   * Apply stored workspace selection if available.
   */
  applyStoredWorkspace(workspaces) {
    if (!this.formData.workspace_id && this.storedWorkspaceId && workspaces.length > 0) {
      const storedWorkspace = workspaces.find((w) => w.id === this.storedWorkspaceId);
      if (storedWorkspace) {
        this.setWorkspace(storedWorkspace);
      }
    }
  }

  /**
   * Apply stored item type selection if available.
   */
  applyStoredItemType() {
    // Don't apply stored item type when creating child items
    if (
      this.storedItemTypeId &&
      this.availableItemTypes.length > 0 &&
      !this.storedItemTypeApplied &&
      !this.restrictedItemTypes
    ) {
      const storedItemType = this.availableItemTypes.find(
        (type) => type.id === this.storedItemTypeId
      );
      if (storedItemType) {
        this.formData.item_type_id = storedItemType.id;
      }
      this.storedItemTypeApplied = true;
    }
  }

  /**
   * Apply config set default item type if no valid stored type.
   */
  applyConfigSetDefault() {
    if (
      this.availableItemTypes.length > 0 &&
      this.currentConfigSet?.default_item_type_id &&
      !this.configSetDefaultApplied
    ) {
      const hasValidStoredType =
        this.storedItemTypeId &&
        this.availableItemTypes.find((type) => type.id === this.storedItemTypeId);
      if (!hasValidStoredType) {
        const configDefault = this.availableItemTypes.find(
          (type) => type.id === this.currentConfigSet.default_item_type_id
        );
        if (configDefault) {
          this.formData.item_type_id = configDefault.id;
        }
      }
      this.configSetDefaultApplied = true;
    }
  }

  // === Deferred Description Image Uploads ===

  /**
   * Track an image inserted before the item exists so it can be uploaded after creation.
   */
  addPendingDescriptionImage(image) {
    if (!image?.file || !image?.url) return;
    this.pendingDescriptionImages = [...this.pendingDescriptionImages, image];
  }

  /**
   * Clear tracked pending images and optionally revoke their local preview URLs.
   */
  clearPendingDescriptionImages(revokeUrls = true) {
    if (revokeUrls && typeof URL !== 'undefined') {
      this.pendingDescriptionImages.forEach((image) => {
        if (image?.url?.startsWith('blob:')) {
          URL.revokeObjectURL(image.url);
        }
      });
    }
    this.pendingDescriptionImages = [];
  }

  // === Validation ===

  /**
   * Validate the form and return whether it's valid.
   */
  validate() {
    const errors = [];

    for (const field of this.screenFields) {
      if (field.is_required) {
        if (field.field_type === 'system') {
          const identifier = field.field_identifier;
          // Skip system-managed fields that are auto-assigned
          if (EXCLUDED_SYSTEM_FIELDS.includes(identifier)) {
            continue;
          }
          const fieldKeyMap = { title: 'name' };
          const formKey = fieldKeyMap[identifier] || identifier;
          const value = this.formData[formKey] ?? this.formData[`${formKey}_id`];
          if (!value) {
            errors.push(`${getSystemFieldName(identifier)} is required`);
          }
        } else if (field.field_type === 'custom') {
          const fieldId = parseInt(field.field_identifier, 10);
          const value = this.customFieldValues[fieldId];
          if (value === undefined || value === null || value === '') {
            const fieldDef = this.allCustomFields.find((f) => f.id === fieldId);
            errors.push(`${fieldDef?.name || 'Custom field'} is required`);
          }
        }
      }
    }

    this.validationErrors = errors;
    return errors.length === 0;
  }

  // === Form Data for API ===

  /**
   * Get form data formatted for the API.
   */
  getFormData() {
    return {
      workspace_id: this.selectedWorkspace?.id || this.formData.workspace_id,
      title: this.formData.name,
      description: this.formData.description || '',
      priority_id: this.formData.priority_id || null,
      milestone_ids: Array.isArray(this.formData.milestone_ids) ? this.formData.milestone_ids : [],
      assignee_id: this.formData.assignee_id || null,
      due_date: this.formData.due_date ? new Date(this.formData.due_date).toISOString() : null,
      start_date: this.formData.start_date
        ? new Date(this.formData.start_date).toISOString()
        : null,
      end_date: this.formData.end_date ? new Date(this.formData.end_date).toISOString() : null,
      status: 'open',
      item_type_id: this.formData.item_type_id,
      parent_id: this.parentItem?.id || null,
      custom_field_values: this.customFieldValues,
    };
  }

  // === Reset ===

  /**
   * Reset form state while keeping loaded reference data.
   */
  resetForm() {
    this.formData = {
      name: '',
      description: '',
      due_date: '',
      start_date: '',
      end_date: '',
      workspace_id: null,
      priority_id: null,
      milestone_ids: [],
      assignee_id: null,
      item_type_id: this.availableItemTypes.length > 0 ? this.availableItemTypes[0].id : null,
    };
    this.customFieldValues = {};
    this.validationErrors = [];
    this.clearPendingDescriptionImages();
    this.selectedWorkspace = null;
    this.parentItem = null;
    this.restrictedItemTypes = null;
    this.workspaceDetails = null;

    // Reset cache keys to force reload for new workspace/item type
    this.configSetLoadedForWorkspace = null;
    this.screenFieldsLoadedForKey = null;
    this.storedItemTypeApplied = false;
    this.configSetDefaultApplied = false;

    // Reset template state (WI-438) so a new modal doesn't inherit the prior
    // type's picker options / mandatory lock.
    this.templateOptions = [];
    this.mandatoryTemplate = null;
    this.selectedTemplateId = null;
    this.#templatesLoadedForKey = null;

    // Keep loaded data (users, milestones, itemTypes, customFields, etc.)
  }

  /**
   * Full reset including all loaded data.
   */
  reset() {
    this.resetForm();

    // Reset all loaded data
    this.users = [];
    this.usersLoaded = false;
    this.allMilestones = [];
    this.milestones = [];
    this.milestonesLoading = false;
    this.milestonesLoaded = false;
    this.milestonesLoadedForKey = null;
    this.itemTypes = [];
    this.hierarchyLevels = [];
    this.availableItemTypes = [];
    this.itemTypesLoaded = false;
    this.allCustomFields = [];
    this.customFields = [];
    this.customFieldsLoaded = false;
    this.screenFields = [];
    this.screenSystemFields = [];
    this.loadingScreenFields = false;
    this.currentConfigSet = null;
    this.#initialized = false;
  }

  // === Initialize ===

  /**
   * Initialize the store (called when form opens).
   * Loads reference data if not already loaded.
   */
  async init() {
    if (this.#initialized) return;

    this.loadStoredSelections();
    await Promise.all([
      this.loadUsers(),
      this.loadMilestones(),
      this.loadItemTypes(),
      this.loadCustomFields(),
    ]);
    this.#initialized = true;
  }

  /**
   * Ensure store is ready to use (call before rendering form).
   */
  async ensureReady() {
    await this.init();
  }
}

export const workItemFormStore = new WorkItemFormStore();
