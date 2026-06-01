<script>
  import { onMount } from 'svelte';
  import { Pencil, RefreshCw, MessageSquare, Bell, HelpCircle, Database, PlusSquare, Box, Globe, Sparkles, Bot, Milestone, UsersRound } from '@lucide/svelte';
  import { toHotkeyString, getShortcutDisplay } from '../../utils/keyboardShortcuts.js';
  import { api } from '../../api.js';
  import FieldSelector from '../../pickers/FieldSelector.svelte';
  import MilestoneCombobox from '../../pickers/MilestoneCombobox.svelte';
  import TriggerNode from './nodes/TriggerNode.svelte';
  import SetFieldNode from './nodes/SetFieldNode.svelte';
  import SetStatusNode from './nodes/SetStatusNode.svelte';
  import AddCommentNode from './nodes/AddCommentNode.svelte';
  import NotifyUserNode from './nodes/NotifyUserNode.svelte';
  import ConditionNode from './nodes/ConditionNode.svelte';
  import UpdateAssetNode from './nodes/UpdateAssetNode.svelte';
  import CreateAssetNode from './nodes/CreateAssetNode.svelte';
  import RelatedItemsNode from './nodes/RelatedItemsNode.svelte';
  import TransitionItemNode from './nodes/TransitionItemNode.svelte';
  import RoundRobinAssignNode from './nodes/RoundRobinAssignNode.svelte';
  import ContainerRunNode from './nodes/ContainerRunNode.svelte';
  import HTTPRequestNode from './nodes/HTTPRequestNode.svelte';
  import AIExtractNode from './nodes/AIExtractNode.svelte';
  import AIAgentNode from './nodes/AIAgentNode.svelte';
  import CreateMilestoneNode from './nodes/CreateMilestoneNode.svelte';
  import CreateMilestoneConfigPanel from './CreateMilestoneConfigPanel.svelte';
  import UpdateAssetConfigPanel from './UpdateAssetConfigPanel.svelte';
  import CreateAssetConfigPanel from './CreateAssetConfigPanel.svelte';
  import PlaceholderReferenceModal from './PlaceholderReferenceModal.svelte';
  import BaseActionFlowEditor from './shared/BaseActionFlowEditor.svelte';
  import { t } from '../../stores/i18n.svelte.js';
  import Checkbox from '../../components/Checkbox.svelte';
  import Select from '../../components/Select.svelte';
  import UserPicker from '../../pickers/UserPicker.svelte';
  import { actionFlowStore } from '../../stores/actionFlowStore.svelte.js';
  import { permissionStore } from '../../stores';
  import { agentRuns } from '../../stores/agentRuns.svelte.js';
  import { infoToast } from '../../stores/toasts.svelte.js';

  let {
    action,
    statuses = [],
    onSave,
    onCancel
  } = $props();

  let showPlaceholderModal = $state(false);
  let milestones = $state([]);
  let teams = $state([]);

  // Actor override: null means the action runs under the triggering user's
  // permissions. Only users with the global action.set_actor permission can
  // change this; others see a read-only display of the current value.
  // svelte-ignore state_referenced_locally
  let actorUserId = $state(action?.actor_user_id ?? null);
  let canSetActor = $derived(permissionStore.hasPermissionKey('action.set_actor'));

  const nodeTypes = {
    trigger: TriggerNode,
    set_field: SetFieldNode,
    set_status: SetStatusNode,
    add_comment: AddCommentNode,
    notify_user: NotifyUserNode,
    condition: ConditionNode,
    update_asset: UpdateAssetNode,
    create_asset: CreateAssetNode,
    related_items: RelatedItemsNode,
    transition_item: TransitionItemNode,
    round_robin_assign: RoundRobinAssignNode,
    container_run: ContainerRunNode,
    http_request: HTTPRequestNode,
    ai_extract: AIExtractNode,
    ai_agent: AIAgentNode,
    create_milestone: CreateMilestoneNode,
  };

  // Mirror each node's accentColor in the minimap so the overview reflects
  // the canvas colour coding instead of rendering every node the same grey.
  const nodeTypeAccents = {
    trigger: 'amber',
    set_field: 'purple',
    set_status: 'teal',
    add_comment: 'orange',
    notify_user: 'magenta',
    condition: 'yellow',
    update_asset: 'teal',
    create_asset: 'green',
    related_items: 'indigo',
    transition_item: 'teal',
    round_robin_assign: 'magenta',
    container_run: 'blue',
    http_request: 'cyan',
    ai_extract: 'purple',
    ai_agent: 'magenta',
    create_milestone: 'green',
  };

  function minimapNodeColor(node) {
    const accent = nodeTypeAccents[node.type];
    if (!accent) return 'var(--ds-accent-gray-subtle, #94a3b8)';
    return `var(--ds-accent-${accent}-subtle)`;
  }

  function minimapNodeStroke(node) {
    const accent = nodeTypeAccents[node.type];
    if (!accent) return 'var(--ds-border, #64748b)';
    return `var(--ds-accent-${accent})`;
  }

  // Map node types to lucide icons (icons stay client-side; everything else
  // about the palette comes from the server-provided catalog so adding a new
  // node type only requires registering it in internal/services/actioncatalog
  // and rebuilding — the editor picks it up automatically). Types missing
  // from this map render with the trigger icon as a neutral fallback.
  const typeIcons = {
    set_field: Pencil,
    set_status: RefreshCw,
    add_comment: MessageSquare,
    notify_user: Bell,
    condition: HelpCircle,
    update_asset: Database,
    create_asset: PlusSquare,
    http_request: Globe,
    container_run: Box,
    ai_extract: Sparkles,
    ai_agent: Bot,
    transition_item: RefreshCw,
    related_items: RefreshCw,
    round_robin_assign: UsersRound,
    create_milestone: Milestone,
  };

  // i18n keys for node types. When a type isn't in this map, the palette
  // falls back to the server-provided label string (English). New node types
  // can ship as catalog-only entries; translations land here in a follow-up.
  const typeI18nKeys = {
    set_field: 'actions.nodes.setField',
    set_status: 'actions.nodes.setStatus',
    add_comment: 'actions.nodes.addComment',
    notify_user: 'actions.nodes.notifyUser',
    condition: 'actions.nodes.condition',
    update_asset: 'actions.nodes.updateAsset',
    create_asset: 'actions.nodes.createAsset',
    http_request: 'actions.nodes.httpRequest',
    container_run: 'actions.nodes.containerRun',
    ai_extract: 'actions.nodes.aiExtract',
    ai_agent: 'actions.nodes.aiAgent',
    transition_item: 'actions.nodes.transitionItem',
    related_items: 'actions.nodes.relatedItems',
    round_robin_assign: 'actions.nodes.roundRobinAssign',
  };

  // nodePalette is built from the catalog response on mount. Trigger nodes
  // are filtered out (they're created implicitly by the editor) along with
  // any type the editor doesn't yet have a custom Svelte node component for.
  let nodePalette = $state([]);
  let triggerTypes = $state([]);

  function buildPaletteFromCatalog(catalog) {
    nodePalette = (catalog?.nodes ?? [])
      .filter((n) => n.type !== 'trigger' && nodeTypes[n.type])
      .map((n) => ({
        type: n.type,
        label: typeI18nKeys[n.type] ? t(typeI18nKeys[n.type]) : n.label,
        icon: typeIcons[n.type] ?? Pencil,
      }));
    triggerTypes = (catalog?.triggers ?? []).map((tr) => ({
      value: tr.type,
      label: triggerI18nKey(tr.type) ? t(triggerI18nKey(tr.type)) : tr.label,
    }));
  }

  function triggerI18nKey(triggerType) {
    switch (triggerType) {
      case 'status_transition': return 'actions.trigger.statusTransition';
      case 'item_created': return 'actions.trigger.itemCreated';
      case 'item_updated': return 'actions.trigger.itemUpdated';
      case 'item_linked': return 'actions.trigger.itemLinked';
      case 'manual': return 'actions.trigger.manual';
      default: return null;
    }
  }

  // Workspace-scoped capability lists for the picker. Loaded once per
  // capability type when the editor mounts, then reused as the user clicks
  // through capability-consuming nodes.
  let capabilitiesByType = $state({
    docker_environment: [],
    http_client: [],
    llm_connection: [],
  });

  async function loadCapabilities(type) {
    if (!action?.workspace_id) return;
    try {
      const list = await api.actionCapabilities.getForWorkspace(action.workspace_id, type);
      capabilitiesByType[type] = list || [];
    } catch (err) {
      console.error(`Failed to load ${type} capabilities for workspace`, err);
    }
  }

  async function loadCatalog() {
    if (!action?.workspace_id) return;
    try {
      const catalog = await api.actions.getCatalog(action.workspace_id);
      buildPaletteFromCatalog(catalog);
    } catch (err) {
      console.error('Failed to load action catalog; palette will be empty', err);
    }
  }

  async function loadMilestones() {
    if (!action?.workspace_id) return;
    try {
      milestones = await api.milestones.getAll({ workspace_id: action.workspace_id, include_global: true }) || [];
    } catch (err) {
      console.error('Failed to load milestones for action editor', err);
      milestones = [];
    }
  }

  async function loadTeams() {
    try {
      teams = await api.teams.getAll() || [];
    } catch (err) {
      console.error('Failed to load teams for action editor', err);
      teams = [];
    }
  }

  onMount(() => {
    if (action?.workspace_id) {
      loadCatalog();
      loadCapabilities('docker_environment');
      loadCapabilities('http_client');
      loadCapabilities('llm_connection');
      loadMilestones();
      loadTeams();
    }

    // Live-reload after every AI chat agent run, regardless of tool calls:
    // any agent activity may have touched this action indirectly, so refetch
    // and rehydrate to reflect the latest server state.
    const unsub = agentRuns.subscribe(async () => {
      if (!action?.id) return;
      try {
        const fresh = await api.get(`/workspaces/${action.workspace_id}/actions/${action.id}`);
        actionFlowStore.init(fresh);
        infoToast(t('actions.aiUpdated', 'Action updated by AI'));
      } catch (err) {
        console.error('Failed to reload action after agent run:', err);
      }
    });
    return unsub;
  });

  function capabilityOptions(type) {
    const empty = [{ value: '', label: t('actions.config.selectCapability') }];
    const list = capabilitiesByType[type] || [];
    if (list.length === 0) {
      return [{ value: '', label: t('actions.config.noCapabilitiesForWorkspace') }];
    }
    return empty.concat(list.map((c) => ({ value: String(c.id), label: c.name })));
  }

  async function handleSave(apiData) {
    // Inject actor override before forwarding to the caller. Backend only
    // enforces action.set_actor when the value actually changes vs the stored
    // action, so passing through unchanged is a no-op.
    apiData.actor_user_id = actorUserId;
    await onSave(apiData);
  }

  // Mapping from FieldSelector IDs to backend column names
  const fieldIdToBackendName = {
    title: 'title',
    description: 'description',
    status: 'status_id',
    priority: 'priority_id',
    assignee: 'assignee_id',
    reporter: 'creator_id',
    milestone: 'milestone_id',
    iteration: 'iteration_id',
    dueDate: 'due_date',
    startDate: 'start_date',
    storyPoints: 'story_points',
    parent: 'parent_id',
    project: 'project_id',
    itemType: 'item_type_id'
  };

  const backendNameToFieldId = Object.fromEntries(
    Object.entries(fieldIdToBackendName).map(([k, v]) => [v, k])
  );

  const standardFieldTypes = {
    title: 'text',
    description: 'text',
    status: 'enum',
    priority: 'enum',
    assignee: 'user',
    reporter: 'user',
    milestone: 'enum',
    iteration: 'enum',
    dueDate: 'date',
    startDate: 'date',
    storyPoints: 'number',
    parent: 'reference',
    project: 'enum',
    itemType: 'enum',
  };

  function getFieldSelectorValue(config) {
    if (config?.target === 'custom_field' && config?.custom_field_id) {
      return {
        id: `cf_${config.custom_field_id}`,
        customFieldId: config.custom_field_id,
        name: config.field_display_name || `Custom field ${config.custom_field_id}`,
        type: config.field_type || '',
        isCustom: true,
      };
    }
    const backendName = config?.field_name;
    if (!backendName) return null;
    if (backendName.startsWith('custom_field_')) {
      const customFieldId = parseInt(backendName.slice('custom_field_'.length), 10);
      return { id: `cf_${customFieldId}`, customFieldId, name: config.field_display_name || backendName, type: config.field_type || '', isCustom: true };
    }
    if (backendName.startsWith('cf_')) {
      return { id: backendName, name: backendName.slice(3), isCustom: true };
    }
    const fieldId = backendNameToFieldId[backendName];
    if (fieldId === 'milestone') {
      return { id: fieldId, name: t('common.milestone', 'Milestone'), type: 'enum' };
    }
    return fieldId
      ? { id: fieldId, name: fieldId, type: standardFieldTypes[fieldId] || '' }
      : { id: backendName, name: backendName, type: config?.field_type || '' };
  }

  function backendFieldName(field) {
    if (!field) return '';
    if (field.customFieldId) return `custom_field_${field.customFieldId}`;
    return fieldIdToBackendName[field.id] || field.id;
  }

  function setFieldConfigForSelection(field) {
    if (field.customFieldId) {
      return {
        target: 'custom_field',
        custom_field_id: field.customFieldId,
        field_name: '',
        field_display_name: field.name,
        field_type: field.type || '',
        value: '',
        value_display_name: '',
      };
    }
    const fieldName = field.id === 'milestone' ? 'milestone_ids' : backendFieldName(field);
    const updates = {
      target: 'column',
      custom_field_id: 0,
      field_name: fieldName,
      field_display_name: field.name,
      field_type: field.type || standardFieldTypes[field.id] || '',
      value: '',
      value_display_name: '',
    };
    if (fieldName === 'milestone_ids') updates.value = '[]';
    return updates;
  }

  function isMilestoneSetField(config) {
    return config?.field_name === 'milestone_ids' || config?.field_name === 'milestone_id';
  }

  function getSetFieldMilestoneIDs(config) {
    const value = config?.value;
    if (Array.isArray(value)) return value.map((id) => parseInt(id, 10)).filter(Boolean);
    if (typeof value === 'number') return [value];
    if (typeof value !== 'string' || value.trim() === '') return [];
    try {
      const parsed = JSON.parse(value);
      if (Array.isArray(parsed)) return parsed.map((id) => parseInt(id, 10)).filter(Boolean);
      const parsedID = parseInt(parsed, 10);
      return parsedID ? [parsedID] : [];
    } catch {
      return value.split(',').map((id) => parseInt(id.trim(), 10)).filter(Boolean);
    }
  }

  function updateSetFieldMilestones(nodeId, ids) {
    const safeIDs = Array.isArray(ids) ? ids : [];
    actionFlowStore.updateNodeConfig(nodeId, {
      field_name: 'milestone_ids',
      field_display_name: t('common.milestone', 'Milestone'),
      field_type: 'enum',
      value: JSON.stringify(safeIDs),
      value_display_name: safeIDs
        .map((id) => milestones.find((m) => m.id === id)?.name)
        .filter(Boolean)
        .join(', '),
    });
  }

  function isUserSetField(config) {
    return config?.field_type === 'user' || config?.field_name === 'assignee_id' || config?.field_name === 'creator_id';
  }

  function getSetFieldUserID(config) {
    const raw = config?.value;
    if (raw === null || raw === undefined || raw === '') return null;
    if (typeof raw === 'number') return raw;
    if (typeof raw === 'string') {
      const trimmed = raw.trim();
      if (/^\d+$/.test(trimmed)) return parseInt(trimmed, 10);
      try {
        const parsed = JSON.parse(trimmed);
        if (typeof parsed === 'number') return parsed;
        if (parsed?.id) return parseInt(parsed.id, 10) || null;
      } catch {
        // Template values like {{item.creator_id}} intentionally stay editable in the text input below.
      }
    }
    if (typeof raw === 'object' && raw?.id) return parseInt(raw.id, 10) || null;
    return null;
  }

  function getUserDisplayName(user) {
    if (!user) return '';
    const fullName = `${user.first_name || ''} ${user.last_name || ''}`.trim();
    return fullName || user.username || user.email || `User #${user.id}`;
  }

  function updateSetFieldUser(nodeId, user) {
    actionFlowStore.updateNodeConfig(nodeId, {
      value: user ? String(user.id) : '',
      value_display_name: user ? getUserDisplayName(user) : '',
    });
  }

  function updateRoundRobinTeam(nodeId, teamId) {
    const parsedTeamId = teamId ? parseInt(teamId, 10) : 0;
    actionFlowStore.updateNodeConfig(nodeId, {
      team_id: parsedTeamId,
      team_name: teams.find((team) => team.id === parsedTeamId)?.name || '',
    });
  }
</script>

<BaseActionFlowEditor
  {action}
  flowStore={actionFlowStore}
  initArgs={[statuses]}
  {nodeTypes}
  {nodePalette}
  {triggerTypes}
  sidebarTitle={t('actions.title')}
  addNodesLabel={t('actions.addNodes')}
  tipsLabel={t('actions.tips')}
  tips={[
    t('actions.tipDragToConnect'),
    t('actions.tipClickToEdit'),
    t('actions.tipConditionBranches'),
  ]}
  nodeConfigLabel={t('actions.nodeConfig')}
  newActionLabel={t('actions.newAction')}
  cancelLabel={t('common.cancel')}
  saveLabel={t('common.save')}
  switchToVerticalLabel={t('actions.switchToVertical')}
  switchToHorizontalLabel={t('actions.switchToHorizontal')}
  saveErrorMessage={t('actions.failedToSave')}
  cancelButtonProps={{
    keyboardHint: getShortcutDisplay('actions', 'cancel'),
    hotkeyConfig: { key: toHotkeyString('actions', 'cancel'), guard: () => !actionFlowStore.saving },
  }}
  saveButtonProps={{
    keyboardHint: getShortcutDisplay('actions', 'save'),
    hotkeyConfig: { key: toHotkeyString('actions', 'save'), guard: () => !actionFlowStore.saving },
  }}
  minimapClass="action-minimap"
  minimapNodeColor={minimapNodeColor}
  minimapNodeStrokeColor={minimapNodeStroke}
  minimapNodeStrokeWidth={2}
  minimapNodeBorderRadius={3}
  minimapMaskColor="var(--action-minimap-mask, rgba(15, 23, 42, 0.55))"
  onSave={handleSave}
  {onCancel}
>
  {#snippet sidebarTop()}
    <h3 class="text-sm font-medium sidebar-title mb-2">{t('actions.runAs')}</h3>
    {#if canSetActor}
      <UserPicker
        bind:value={actorUserId}
        placeholder={t('actions.runAsTriggerUser')}
        showUnassigned={true}
        unassignedLabel={t('actions.runAsTriggerUser')}
        onSelect={(user) => { actorUserId = user?.id ?? null; }}
      />
      <p class="mt-2 text-xs sidebar-hints">{t('actions.runAsHint')}</p>
    {:else if action?.actor_user_id && action?.actor_name}
      <div class="text-xs sidebar-subtitle">
        <div class="font-medium" style="color: var(--ds-text);">{action.actor_name}</div>
        <div class="mt-1">{t('actions.runAsReadonlyHint')}</div>
      </div>
    {:else}
      <p class="text-xs sidebar-hints">{t('actions.runAsTriggerUser')}</p>
    {/if}
  {/snippet}

  {#snippet triggerConfig(selectedNode, store)}
    <div>
      <label for="config-trigger-type" class="block text-xs font-medium mb-1">{t('actions.config.triggerType')}</label>
      <Select
        id="config-trigger-type"
        options={triggerTypes}
        value={selectedNode.data?.triggerType || action?.trigger_type || 'status_transition'}
        onchange={(v) => {
          store.updateNodeData(selectedNode.id, { triggerType: v });
          store.updateTriggerType(v);
        }}
        size="small"
      />
    </div>
    {#if (selectedNode.data?.triggerType || action?.trigger_type) === 'status_transition'}
      <div>
        <label for="config-from-status" class="block text-xs font-medium mb-1">{t('actions.config.fromStatus')}</label>
        <Select
          id="config-from-status"
          options={[{ value: '', label: t('actions.config.anyStatus') }, ...statuses.map(s => ({ value: s.id, label: s.name }))]}
          value={selectedNode.data?.config?.from_status_id || ''}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { from_status_id: v ? parseInt(v) : null })}
          size="small"
        />
      </div>
      <div>
        <label for="config-to-status" class="block text-xs font-medium mb-1">{t('actions.config.toStatus')}</label>
        <Select
          id="config-to-status"
          options={[{ value: '', label: t('actions.config.anyStatus') }, ...statuses.map(s => ({ value: s.id, label: s.name }))]}
          value={selectedNode.data?.config?.to_status_id || ''}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { to_status_id: v ? parseInt(v) : null })}
          size="small"
        />
      </div>
    {/if}
    {#if (selectedNode.data?.triggerType || action?.trigger_type) === 'item_updated'}
      <div>
        <div class="block text-xs font-medium mb-1">{t('actions.config.triggerField')}</div>
        <FieldSelector
          placeholder={t('actions.config.anyField')}
          selectedField={getFieldSelectorValue(selectedNode.data?.config)}
          onSelect={(field) => {
            store.updateNodeConfig(selectedNode.id, { field_name: backendFieldName(field) });
          }}
          onClear={() => store.updateNodeConfig(selectedNode.id, { field_name: '' })}
        />
      </div>
    {/if}
    <div class="pt-4 border-t cascade-option">
      <Checkbox
        checked={selectedNode.data?.config?.respond_to_cascades || false}
        onchange={(checked) => store.updateNodeConfig(selectedNode.id, { respond_to_cascades: checked })}
        label={t('actions.trigger.respondToCascades')}
        hint={t('actions.trigger.respondToCascadesHint')}
        size="small"
      />
    </div>
  {/snippet}

  {#snippet nodeConfig(selectedNode, store, _handleDeleteNode)}
    {#if selectedNode.type === 'set_status'}
      <div>
        <label for="config-target-status" class="block text-xs font-medium mb-1">{t('actions.config.targetStatus')}</label>
        <Select
          id="config-target-status"
          options={[{ value: '', label: t('actions.config.selectStatus') }, ...statuses.map(s => ({ value: s.id, label: s.name }))]}
          value={selectedNode.data?.config?.status_id || ''}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { status_id: parseInt(v) })}
          size="small"
        />
      </div>
    {:else if selectedNode.type === 'set_field'}
      <div>
        <label for="config-set-field-name" class="block text-xs font-medium mb-1">{t('actions.config.fieldName')}</label>
        <FieldSelector
          selectedField={getFieldSelectorValue(selectedNode.data?.config)}
          onSelect={(field) => {
            store.updateNodeConfig(selectedNode.id, setFieldConfigForSelection(field));
          }}
          onClear={() => store.updateNodeConfig(selectedNode.id, { target: 'column', custom_field_id: 0, field_name: '', field_display_name: '', field_type: '', value: '', value_display_name: '' })}
        />
      </div>
      <div>
        <div class="flex items-center gap-1 mb-1">
          <label for="config-set-field-value" class="block text-xs font-medium">{t('actions.config.value')}</label>
          <button
            onclick={() => showPlaceholderModal = true}
            class="text-[var(--ds-text-subtlest)] hover:text-[var(--ds-interactive)] transition-colors"
            title={t('actions.placeholders.showReference')}
          >
            <HelpCircle class="w-3.5 h-3.5" />
          </button>
        </div>
        {#if isMilestoneSetField(selectedNode.data?.config)}
          <MilestoneCombobox
            value={getSetFieldMilestoneIDs(selectedNode.data?.config)}
            workspaceId={action?.workspace_id}
            multiple={true}
            placeholder={t('pickers.selectMilestones', 'Select milestones')}
            onSelect={({ ids }) => updateSetFieldMilestones(selectedNode.id, ids)}
          />
          <p class="text-xs mt-1 sidebar-hints">
            {t('actions.config.milestonePickerHint')}
          </p>
        {:else if isUserSetField(selectedNode.data?.config)}
          <UserPicker
            value={getSetFieldUserID(selectedNode.data?.config)}
            workspaceId={action?.workspace_id}
            placeholder={t('pickers.selectUser')}
            showUnassigned={true}
            onSelect={(user) => updateSetFieldUser(selectedNode.id, user)}
          />
          <p class="text-xs mt-1 sidebar-hints">
            {t('actions.config.userPickerHint')}
          </p>
          <input
            id="config-set-field-value"
            type="text"
            class="w-full px-3 py-2 border rounded-md text-sm config-input mt-2"
            value={selectedNode.data?.config?.value || ''}
            oninput={(e) => store.updateNodeConfig(selectedNode.id, { value: e.currentTarget.value, value_display_name: '' })}
            placeholder="{'{{'}item.creator_id{'}}'}"
          />
        {:else}
          <input
            id="config-set-field-value"
            type="text"
            class="w-full px-3 py-2 border rounded-md text-sm config-input"
            value={selectedNode.data?.config?.value || ''}
            oninput={(e) => store.updateNodeConfig(selectedNode.id, { value: e.currentTarget.value, value_display_name: '' })}
            placeholder="{'{{'}item.creator_id{'}}'}"
          />
        {/if}
      </div>
    {:else if selectedNode.type === 'add_comment'}
      <div>
        <div class="flex items-center gap-1 mb-1">
          <label for="config-comment-content" class="block text-xs font-medium">{t('actions.config.commentContent')}</label>
          <button
            onclick={() => showPlaceholderModal = true}
            class="text-[var(--ds-text-subtlest)] hover:text-[var(--ds-interactive)] transition-colors"
            title={t('actions.placeholders.showReference')}
          >
            <HelpCircle class="w-3.5 h-3.5" />
          </button>
        </div>
        <textarea
          id="config-comment-content"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          rows="4"
          value={selectedNode.data?.config?.content || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { content: e.currentTarget.value })}
          placeholder={t('actions.config.commentPlaceholder')}
        ></textarea>
      </div>
      <Checkbox
        checked={selectedNode.data?.config?.is_private || false}
        onchange={(checked) => store.updateNodeConfig(selectedNode.id, { is_private: checked })}
        label={t('actions.config.privateComment')}
        size="small"
      />
    {:else if selectedNode.type === 'condition'}
      <div>
        <label for="config-condition-field" class="block text-xs font-medium mb-1">{t('actions.config.fieldToCheck')}</label>
        <FieldSelector
          selectedField={getFieldSelectorValue(selectedNode.data?.config)}
          onSelect={(field) => store.updateNodeConfig(selectedNode.id, { field_name: backendFieldName(field) })}
          onClear={() => store.updateNodeConfig(selectedNode.id, { field_name: '' })}
        />
      </div>
      <div>
        <label for="config-condition-operator" class="block text-xs font-medium mb-1">{t('actions.config.operator')}</label>
        <Select
          id="config-condition-operator"
          options={[
            { value: 'eq', label: t('actions.operators.equals') },
            { value: 'ne', label: t('actions.operators.notEquals') },
            { value: 'contains', label: t('actions.operators.contains') },
            { value: 'gt', label: t('actions.operators.greaterThan') },
            { value: 'lt', label: t('actions.operators.lessThan') },
            { value: 'is_empty', label: t('actions.operators.isEmpty') },
            { value: 'is_not_empty', label: t('actions.operators.isNotEmpty') }
          ]}
          value={selectedNode.data?.config?.operator || 'eq'}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { operator: v })}
          size="small"
        />
      </div>
      <div>
        <label for="config-condition-value" class="block text-xs font-medium mb-1">{t('actions.config.compareValue')}</label>
        <input
          id="config-condition-value"
          type="text"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          value={selectedNode.data?.config?.value || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { value: e.currentTarget.value })}
        />
      </div>
    {:else if selectedNode.type === 'notify_user'}
      <div>
        <label for="config-recipient-type" class="block text-xs font-medium mb-1">{t('actions.config.recipientType')}</label>
        <Select
          id="config-recipient-type"
          options={[
            { value: 'assignee', label: t('actions.recipients.assignee') },
            { value: 'creator', label: t('actions.recipients.creator') },
            { value: 'specific', label: t('actions.recipients.specific') }
          ]}
          value={selectedNode.data?.config?.recipient_type || selectedNode.data?.config?.recipients?.[0] || 'assignee'}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { recipient_type: v, recipients: v === 'specific' ? [] : [v] })}
          size="small"
        />
      </div>
      <div>
        <div class="flex items-center gap-1 mb-1">
          <label for="config-notify-message" class="block text-xs font-medium">{t('actions.config.notifyMessage')}</label>
          <button
            onclick={() => showPlaceholderModal = true}
            class="text-[var(--ds-text-subtlest)] hover:text-[var(--ds-interactive)] transition-colors"
            title={t('actions.placeholders.showReference')}
          >
            <HelpCircle class="w-3.5 h-3.5" />
          </button>
        </div>
        <textarea
          id="config-notify-message"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          rows="4"
          value={selectedNode.data?.config?.message || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { message: e.currentTarget.value })}
          placeholder={t('actions.config.notifyPlaceholder')}
        ></textarea>
      </div>
      <Checkbox
        checked={selectedNode.data?.config?.include_link ?? true}
        onchange={(checked) => store.updateNodeConfig(selectedNode.id, { include_link: checked })}
        label={t('actions.config.includeLink')}
        size="small"
      />
    {:else if selectedNode.type === 'update_asset'}
      <UpdateAssetConfigPanel {selectedNode} bind:showPlaceholderModal />
    {:else if selectedNode.type === 'create_asset'}
      <CreateAssetConfigPanel {selectedNode} bind:showPlaceholderModal />
    {:else if selectedNode.type === 'create_milestone'}
      <CreateMilestoneConfigPanel {selectedNode} bind:showPlaceholderModal />
    {:else if selectedNode.type === 'related_items'}
      <div>
        <label for="config-related-relation" class="block text-xs font-medium mb-1">Relation</label>
        <Select
          id="config-related-relation"
          options={[
            { value: 'descendants', label: 'Descendants' },
            { value: 'direct_children', label: 'Direct children' },
            { value: 'ancestors', label: 'Ancestors' },
            { value: 'linked', label: 'Linked items' },
          ]}
          value={selectedNode.data?.config?.relation || 'descendants'}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { relation: v })}
          size="small"
        />
      </div>
      <Checkbox
        checked={selectedNode.data?.config?.cross_workspace || false}
        onchange={(checked) => store.updateNodeConfig(selectedNode.id, { cross_workspace: checked })}
        label="Cross workspace"
        size="small"
      />
    {:else if selectedNode.type === 'round_robin_assign'}
      <div>
        <label for="config-round-robin-team" class="block text-xs font-medium mb-1">Team</label>
        <Select
          id="config-round-robin-team"
          options={[{ value: '', label: 'Select team' }, ...teams.map(team => ({ value: team.id, label: team.name }))]}
          value={selectedNode.data?.config?.team_id || ''}
          onchange={(v) => updateRoundRobinTeam(selectedNode.id, v)}
          size="small"
        />
      </div>
      <Checkbox
        checked={selectedNode.data?.config?.skip_on_leave_members ?? true}
        onchange={(checked) => store.updateNodeConfig(selectedNode.id, { skip_on_leave_members: checked })}
        label="Skip members on leave"
        size="small"
      />
      <Checkbox
        checked={selectedNode.data?.config?.use_leave_substitutes ?? true}
        onchange={(checked) => store.updateNodeConfig(selectedNode.id, { use_leave_substitutes: checked })}
        label="Use leave substitutes"
        size="small"
      />
    {:else if selectedNode.type === 'transition_item'}
      {@const target = selectedNode.data?.config?.target || { mode: 'matching_terminal' }}
      <div>
        <label for="config-transition-mode" class="block text-xs font-medium mb-1">Transition target</label>
        <Select
          id="config-transition-mode"
          options={[
            { value: 'matching_terminal', label: 'Match trigger terminal category' },
            { value: 'explicit', label: 'Explicit status' },
            { value: 'category_name', label: 'Terminal by category name' },
          ]}
          value={target.mode || 'matching_terminal'}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { target: { ...target, mode: v } })}
          size="small"
        />
      </div>
      {#if (target.mode || 'matching_terminal') === 'explicit'}
        <div>
          <label for="config-transition-status" class="block text-xs font-medium mb-1">{t('actions.config.targetStatus')}</label>
          <Select
            id="config-transition-status"
            options={[{ value: '', label: t('actions.config.selectStatus') }, ...statuses.map(s => ({ value: s.id, label: s.name }))]}
            value={target.status_id || ''}
            onchange={(v) => store.updateNodeConfig(selectedNode.id, { target: { ...target, status_id: v ? parseInt(v) : 0 } })}
            size="small"
          />
        </div>
      {:else if target.mode === 'category_name'}
        <div>
          <label for="config-transition-category" class="block text-xs font-medium mb-1">Category name</label>
          <input
            id="config-transition-category"
            type="text"
            class="w-full px-3 py-2 border rounded-md text-sm config-input"
            value={target.category_name || ''}
            oninput={(e) => store.updateNodeConfig(selectedNode.id, { target: { ...target, category_name: e.currentTarget.value } })}
            placeholder="Done"
          />
        </div>
      {/if}
      <Checkbox
        checked={selectedNode.data?.config?.skip_if_already_matching ?? true}
        onchange={(checked) => store.updateNodeConfig(selectedNode.id, { skip_if_already_matching: checked })}
        label="Skip if already matching"
        size="small"
      />
    {:else if selectedNode.type === 'container_run'}
      <div>
        <label for="config-container-cap" class="block text-xs font-medium mb-1">{t('actions.config.dockerCapability')}</label>
        <Select
          id="config-container-cap"
          options={capabilityOptions('docker_environment')}
          value={selectedNode.data?.config?.capability_id ? String(selectedNode.data.config.capability_id) : ''}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { capability_id: v ? parseInt(v) : null })}
          size="small"
        />
      </div>
      <div>
        <label for="config-container-output" class="block text-xs font-medium mb-1">{t('actions.config.outputField')}</label>
        <input
          id="config-container-output"
          type="text"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          value={selectedNode.data?.config?.output_field || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { output_field: e.currentTarget.value })}
          placeholder={t('actions.config.outputFieldPlaceholder')}
        />
      </div>
      <div>
        <label for="config-container-timeout" class="block text-xs font-medium mb-1">{t('actions.config.timeoutSecs')}</label>
        <input
          id="config-container-timeout"
          type="number"
          min="1"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          value={selectedNode.data?.config?.timeout_secs || 60}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { timeout_secs: parseInt(e.currentTarget.value) || 60 })}
        />
      </div>
    {:else if selectedNode.type === 'http_request'}
      <div>
        <label for="config-http-cap" class="block text-xs font-medium mb-1">{t('actions.config.httpCapability')}</label>
        <Select
          id="config-http-cap"
          options={capabilityOptions('http_client')}
          value={selectedNode.data?.config?.capability_id ? String(selectedNode.data.config.capability_id) : ''}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { capability_id: v ? parseInt(v) : undefined })}
          size="small"
        />
      </div>
      <div>
        <label for="config-http-method" class="block text-xs font-medium mb-1">{t('actions.config.httpMethod')}</label>
        <Select
          id="config-http-method"
          options={[
            { value: 'GET', label: 'GET' },
            { value: 'POST', label: 'POST' },
            { value: 'PUT', label: 'PUT' },
            { value: 'PATCH', label: 'PATCH' },
            { value: 'DELETE', label: 'DELETE' },
          ]}
          value={selectedNode.data?.config?.method || 'GET'}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { method: v })}
          size="small"
        />
      </div>
      <div>
        <div class="flex items-center gap-1 mb-1">
          <label for="config-http-url" class="block text-xs font-medium">{t('actions.config.urlTemplate')}</label>
          <button
            onclick={() => showPlaceholderModal = true}
            class="text-[var(--ds-text-subtlest)] hover:text-[var(--ds-interactive)] transition-colors"
            title={t('actions.placeholders.showReference')}
          >
            <HelpCircle class="w-3.5 h-3.5" />
          </button>
        </div>
        <input
          id="config-http-url"
          type="text"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          value={selectedNode.data?.config?.url_template || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { url_template: e.currentTarget.value })}
          placeholder="https://example.com/api/items/{'{{'}item.id{'}}'}"
        />
      </div>
      <div>
        <label for="config-http-body" class="block text-xs font-medium mb-1">{t('actions.config.requestBody')}</label>
        <textarea
          id="config-http-body"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          rows="3"
          value={selectedNode.data?.config?.body || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { body: e.currentTarget.value })}
          placeholder={t('actions.config.requestBodyPlaceholder')}
        ></textarea>
      </div>
      <div>
        <label for="config-http-output" class="block text-xs font-medium mb-1">{t('actions.config.outputField')}</label>
        <input
          id="config-http-output"
          type="text"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          value={selectedNode.data?.config?.output_field || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { output_field: e.currentTarget.value })}
          placeholder="response"
        />
      </div>
    {:else if selectedNode.type === 'ai_extract'}
      <div>
        <label for="config-aix-cap" class="block text-xs font-medium mb-1">{t('actions.config.llmCapability')}</label>
        <Select
          id="config-aix-cap"
          options={capabilityOptions('llm_connection')}
          value={selectedNode.data?.config?.capability_id ? String(selectedNode.data.config.capability_id) : ''}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { capability_id: v ? parseInt(v) : null })}
          size="small"
        />
      </div>
      <div>
        <label for="config-aix-input" class="block text-xs font-medium mb-1">{t('actions.config.inputField')}</label>
        <input
          id="config-aix-input"
          type="text"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          value={selectedNode.data?.config?.input_field || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { input_field: e.currentTarget.value })}
          placeholder={t('actions.config.inputFieldPlaceholder')}
        />
      </div>
      <div>
        <label for="config-aix-prompt" class="block text-xs font-medium mb-1">{t('actions.config.aiPrompt')}</label>
        <textarea
          id="config-aix-prompt"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          rows="4"
          value={selectedNode.data?.config?.prompt || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { prompt: e.currentTarget.value })}
          placeholder={t('actions.config.aiExtractPromptPlaceholder')}
        ></textarea>
      </div>
      <div>
        <label for="config-aix-schema" class="block text-xs font-medium mb-1">{t('actions.config.outputSchema')}</label>
        <textarea
          id="config-aix-schema"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          rows="4"
          value={selectedNode.data?.config?.output_schema || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { output_schema: e.currentTarget.value })}
          placeholder={'{"type":"object","properties":{...}}'}
          style="font-family: monospace;"
        ></textarea>
      </div>
      <div>
        <label for="config-aix-output" class="block text-xs font-medium mb-1">{t('actions.config.outputField')}</label>
        <input
          id="config-aix-output"
          type="text"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          value={selectedNode.data?.config?.output_field || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { output_field: e.currentTarget.value })}
          placeholder="extracted_data"
        />
      </div>
    {:else if selectedNode.type === 'ai_agent'}
      <div>
        <label for="config-aia-cap" class="block text-xs font-medium mb-1">{t('actions.config.llmCapability')}</label>
        <Select
          id="config-aia-cap"
          options={capabilityOptions('llm_connection')}
          value={selectedNode.data?.config?.capability_id ? String(selectedNode.data.config.capability_id) : ''}
          onchange={(v) => store.updateNodeConfig(selectedNode.id, { capability_id: v ? parseInt(v) : null })}
          size="small"
        />
      </div>
      <div>
        <label for="config-aia-prompt" class="block text-xs font-medium mb-1">{t('actions.config.systemPrompt')}</label>
        <textarea
          id="config-aia-prompt"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          rows="4"
          value={selectedNode.data?.config?.prompt || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { prompt: e.currentTarget.value })}
          placeholder={t('actions.config.systemPromptPlaceholder')}
        ></textarea>
      </div>
      <div>
        <label for="config-aia-input-fields" class="block text-xs font-medium mb-1">{t('actions.config.inputFields')}</label>
        <input
          id="config-aia-input-fields"
          type="text"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          value={(selectedNode.data?.config?.input_fields || []).join(', ')}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, {
            input_fields: e.currentTarget.value.split(',').map((s) => s.trim()).filter(Boolean),
          })}
          placeholder={t('actions.config.inputFieldsPlaceholder')}
        />
      </div>
      <div>
        <label for="config-aia-tools" class="block text-xs font-medium mb-1">{t('actions.config.agentTools')}</label>
        <p class="text-xs mb-2" style="color: var(--ds-text-subtle);">{t('actions.config.agentToolsHint')}</p>
        {#if (capabilitiesByType.http_client || []).length === 0}
          <p class="text-xs" style="color: var(--ds-text-subtle); font-style: italic;">{t('actions.config.noToolsAvailable')}</p>
        {:else}
          {#each capabilitiesByType.http_client as cap}
            <label class="flex items-center gap-2 text-sm py-1 cursor-pointer">
              <input
                type="checkbox"
                checked={(selectedNode.data?.config?.tools || []).includes(String(cap.id))}
                onchange={(e) => {
                  const current = selectedNode.data?.config?.tools || [];
                  const next = e.currentTarget.checked
                    ? [...current.filter((id) => id !== String(cap.id)), String(cap.id)]
                    : current.filter((id) => id !== String(cap.id));
                  store.updateNodeConfig(selectedNode.id, { tools: next });
                }}
              />
              <span style="color: var(--ds-text);">{cap.name}</span>
            </label>
          {/each}
        {/if}
      </div>
      <div>
        <label for="config-aia-max-steps" class="block text-xs font-medium mb-1">{t('actions.config.maxSteps')}</label>
        <input
          id="config-aia-max-steps"
          type="number"
          min="1"
          max="50"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          value={selectedNode.data?.config?.max_steps || 10}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { max_steps: parseInt(e.currentTarget.value) || 10 })}
        />
      </div>
      <div>
        <label for="config-aia-output" class="block text-xs font-medium mb-1">{t('actions.config.outputField')}</label>
        <input
          id="config-aia-output"
          type="text"
          class="w-full px-3 py-2 border rounded-md text-sm config-input"
          value={selectedNode.data?.config?.output_field || ''}
          oninput={(e) => store.updateNodeConfig(selectedNode.id, { output_field: e.currentTarget.value })}
          placeholder="agent_answer"
        />
      </div>
    {/if}
  {/snippet}
</BaseActionFlowEditor>

{#if showPlaceholderModal}
  <PlaceholderReferenceModal onclose={() => showPlaceholderModal = false} />
{/if}

<style>
  :global(.action-minimap) {
    background-color: var(--ds-surface-raised) !important;
    border: 1px solid var(--ds-border) !important;
    border-radius: 8px !important;
    box-shadow: var(--shadow-md) !important;
    overflow: hidden;
  }

  :global(.action-minimap .svelte-flow__minimap-mask) {
    fill: var(--action-minimap-mask, rgba(15, 23, 42, 0.55));
  }

  .cascade-option {
    border-color: var(--ds-border);
  }
</style>
