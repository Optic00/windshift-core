<script>
  import { t } from '../../stores/i18n.svelte.js';
  import { collectionEditorOptions } from '../../stores/collectionEditorOptions.svelte.js';
  import { collectionFieldLinks } from '../../stores/collectionFieldLinks.svelte.js';
  import { api } from '../../api.js';
  import InlineFieldEditor from '../../editors/InlineFieldEditor.svelte';
  import ItemPicker from '../../pickers/ItemPicker.svelte';
  import UserPicker from '../../pickers/UserPicker.svelte';
  import MilestoneCombobox from '../../pickers/MilestoneCombobox.svelte';
  import ItemKey from '../items/ItemKey.svelte';
  import ColorDot from '../../components/ColorDot.svelte';
  import Lozenge from '../../components/Lozenge.svelte';
  import Checkbox from '../../components/Checkbox.svelte';
  import ListCustomFieldCell from './ListCustomFieldCell.svelte';
  import { Calendar, User, Target, Globe, Building2, FolderKanban } from '@lucide/svelte';
  import ItemTypeIcon from '../../components/ItemTypeIcon.svelte';
  import { formatDate, formatDateOnly } from '../../utils/dateFormatter.js';
  import {
    createStatusPickerConfig,
    priorityPickerConfig as priorityConfig,
    iterationPickerConfig as iterationConfig,
    projectPickerConfig as projectConfig,
  } from '../../pickers/pickerConfigs.js';

  let {
    item,
    column,
    workspace,
    collectionId = null,
    canEdit = true,
    statuses = [],
    statusCategories = [],
    priorities = [],
    milestones = [],
    iterations = [],
    users = [],
    projects = [],
    itemTypes = [],
    customFieldDefinitions = [],
    onitemUpdated = undefined,
    onupdateError = undefined,
  } = $props();

  // Get the field definition for custom fields
  let fieldDefinition = $derived(
    column.field_type === 'custom'
      ? customFieldDefinitions.find(f => String(f.id) === column.field_identifier)
      : null
  );

  // Never use page-level editable options for a row in a mixed-workspace
  // collection. The cache is keyed by the owning workspace and each family is
  // loaded only when its picker first opens.
  let editorOptions = $derived(collectionEditorOptions.get(item.workspace_id));
  let fieldLinks = $derived(
    fieldDefinition?.field_type === 'linking'
      ? collectionFieldLinks.getFieldLinks(item.id, fieldDefinition.id, fieldDefinition.options)
      : [],
  );

  async function reloadFieldLinks(change) {
    await collectionFieldLinks.refreshForItems([item.id, ...(change?.itemIds || [])]);
  }

  // Get custom field value from item
  function getCustomFieldValue(item, fieldIdentifier) {
    if (!item.custom_field_values) return null;
    return item.custom_field_values[fieldIdentifier] ?? null;
  }

  // Handle item updates
  async function handleItemUpdate(field, value) {
    try {
      const updatedItem = field === 'status_id'
        ? await api.items.transition(item.id, value)
        : await api.items.update(item.id, { [field]: value });
      onitemUpdated?.({ item: updatedItem, field, value });
    } catch (error) {
      onupdateError?.({ error: error.message, field, value });
    }
  }

  // Handle custom field updates
  async function handleCustomFieldUpdate(fieldIdentifier, value) {
    try {
      const currentCustomValues = item.custom_field_values || {};
      const updatedItem = await api.items.update(item.id, {
        custom_field_values: {
          ...currentCustomValues,
          [fieldIdentifier]: value
        }
      });
      onitemUpdated?.({ item: updatedItem, field: fieldIdentifier, value });
    } catch (error) {
      onupdateError?.({ error: error.message, field: fieldIdentifier, value });
    }
  }

  // Handle task checkbox toggle
  async function toggleTaskStatus(isCompleted) {
    const newStatus = isCompleted ? 'completed' : 'open';
    try {
      await api.items.update(item.id, { status: newStatus });
      item.status = newStatus;
      onitemUpdated?.({ item, field: 'status', value: newStatus });
    } catch (error) {
      onupdateError?.({ error: error.message, field: 'status', value: newStatus });
    }
  }

  // Configs for pickers
  const statusConfig = $derived(createStatusPickerConfig(statusCategories));
</script>

{#if column.field_type === 'workspace'}
  <span class="block truncate text-sm" style="color: var(--ds-text);" title={workspace?.name || item.workspace_name || ''}>
    {workspace?.name || item.workspace_name || '-'}
  </span>

{:else if column.field_type === 'system'}
  {#if column.field_identifier === 'key'}
    <!-- Item Key -->
    <div class="flex items-center gap-2 min-w-0">
      <ItemKey
        {item}
        {workspace}
        href={collectionId && workspace
          ? `/workspaces/${workspace.id}/collections/${collectionId}/items/${item.id}`
          : `/workspaces/${workspace?.id || item.workspace_id}/items/${item.id}`}
      />
    </div>

  {:else if column.field_identifier === 'title'}
    <!-- Title with type icon -->
    <div class="flex items-center gap-2 min-w-0">
      {#if item.item_type_id && itemTypes.length > 0}
        {@const itemType = itemTypes.find(type => type.id === item.item_type_id)}
        {#if itemType}
          <ItemTypeIcon {itemType} />
        {/if}
      {/if}
      <div class="flex-1 min-w-0" data-testid={`workspace-item-title-${item.id}`}>
        {#if canEdit}
          <InlineFieldEditor
            {item}
            field="title"
            fieldType="text"
            placeholder="Enter title..."
            required={true}
            className=""
            onitemUpdated={(detail) => onitemUpdated?.(detail)}
            onupdateError={(detail) => onupdateError?.(detail)}
          />
        {:else}
          <span class="truncate text-sm" style="color: var(--ds-text);">{item.title}</span>
        {/if}
      </div>
    </div>

  {:else if column.field_identifier === 'status'}
    <!-- Status / Task Checkbox -->
    {#if item.is_task}
      <Checkbox
        checked={item.status === 'completed'}
        onchange={(checked) => toggleTaskStatus(checked)}
        label={item.status === 'completed' ? 'Done' : 'Todo'}
        size="small"
        disabled={!canEdit}
      />
    {:else}
      {@const selectedStatus = [...editorOptions.statuses, ...statuses].find(s => s.id === item.status_id) || (item.status_name ? { name: item.status_name, category_id: null } : null)}
      {@const statusCategory = selectedStatus ? statusCategories.find(sc => sc.id === selectedStatus.category_id) : null}
      {#if canEdit}
        <ItemPicker
          value={item.status_id}
          items={editorOptions.statuses}
          loading={editorOptions.loading.statuses}
          config={statusConfig}
          placeholder="Set status"
          showUnassigned={false}
          allowClear={false}
          onOpen={() => collectionEditorOptions.load(item.workspace_id, 'statuses')}
          onSelect={async (selected) => {
            const statusId = selected?.id;
            if (statusId && statusId !== item.status_id) {
              await handleItemUpdate('status_id', statusId);
            }
          }}
        >
          {#snippet children()}
            <span class="cursor-pointer">
              <Lozenge
                text={selectedStatus ? selectedStatus.name : 'Set status'}
                customBg={statusCategory?.color || '#6b7280'}
              />
            </span>
          {/snippet}
        </ItemPicker>
      {:else}
        <Lozenge
          text={selectedStatus ? selectedStatus.name : '-'}
          customBg={statusCategory?.color || '#6b7280'}
        />
      {/if}
    {/if}

  {:else if column.field_identifier === 'priority'}
    <!-- Priority -->
    {@const selectedPriority = [...editorOptions.priorities, ...priorities].find(p => p.id === item.priority_id) || (item.priority_name ? { name: item.priority_name, color: item.priority_color } : null)}
    {#if canEdit}
      <ItemPicker
        value={item.priority_id}
        items={editorOptions.priorities}
        loading={editorOptions.loading.priorities}
        config={priorityConfig}
        placeholder="Select priority"
        showUnassigned={true}
        unassignedLabel="No priority"
        allowClear={true}
        onOpen={() => collectionEditorOptions.load(item.workspace_id, 'priorities')}
        onSelect={async (selected) => {
          const priorityId = selected?.id || null;
          await handleItemUpdate('priority_id', priorityId);
        }}
      >
        {#snippet children()}
          {#if selectedPriority}
            <span
              class="w-full flex items-center justify-start gap-2 text-sm text-left cursor-pointer"
              style="color: {selectedPriority.color || 'var(--ds-text-subtle)'};"
            >
              <ColorDot color={selectedPriority.color} />
              {selectedPriority.name}
            </span>
          {:else}
            <span
              class="w-full flex items-center justify-start gap-2 text-sm text-left cursor-pointer"
              style="color: var(--ds-text-subtle);"
            >
              {t('pickers.selectPriority')}
            </span>
          {/if}
        {/snippet}
      </ItemPicker>
    {:else}
      {#if selectedPriority}
        <span class="flex items-center gap-2 text-sm" style="color: {selectedPriority.color || 'var(--ds-text-subtle)'};">
          <ColorDot color={selectedPriority.color} />
          {selectedPriority.name}
        </span>
      {:else}
        <span class="text-sm" style="color: var(--ds-text-subtle);">-</span>
      {/if}
    {/if}

  {:else if column.field_identifier === 'assignee'}
    <!-- Assignee -->
    {@const assignee = [...editorOptions.users, ...users].find(u => u.id === item.assignee_id)}
    {#if canEdit}
      <UserPicker
        value={item.assignee_id}
        placeholder="Assign"
        showUnassigned={true}
        users={editorOptions.users}
        loading={editorOptions.loading.users}
        onOpen={() => collectionEditorOptions.load(item.workspace_id, 'users')}
        onSelect={async (selectedUser) => {
          const userId = selectedUser?.id || null;
          await handleItemUpdate('assignee_id', userId);
        }}
      >
        {#snippet children()}
          {#if assignee}
            <div class="flex items-center gap-2 cursor-pointer" data-testid={`list-cell-assignee-${item.id}`}>
              <div class="w-5 h-5 rounded-full bg-blue-500 flex items-center justify-center text-white text-[10px] font-medium">
                {(assignee.first_name?.[0] || '') + (assignee.last_name?.[0] || '') || assignee.username?.[0]?.toUpperCase() || '?'}
              </div>
              <span class="text-sm truncate" style="color: var(--ds-text);">
                {assignee.first_name} {assignee.last_name}
              </span>
            </div>
          {:else}
            <span class="flex items-center gap-2 text-sm cursor-pointer" style="color: var(--ds-text-subtle);" data-testid={`list-cell-assignee-${item.id}`}>
              <User class="w-4 h-4" />
              {t('pickers.assignee')}
            </span>
          {/if}
        {/snippet}
      </UserPicker>
    {:else}
      {#if assignee}
        <div class="flex items-center gap-2">
          <div class="w-5 h-5 rounded-full bg-blue-500 flex items-center justify-center text-white text-[10px] font-medium">
            {(assignee.first_name?.[0] || '') + (assignee.last_name?.[0] || '') || assignee.username?.[0]?.toUpperCase() || '?'}
          </div>
          <span class="text-sm truncate" style="color: var(--ds-text);">
            {assignee.first_name} {assignee.last_name}
          </span>
        </div>
      {:else if item.assignee_name}
        <div class="flex items-center gap-2">
          <User class="w-4 h-4" style="color: var(--ds-text-subtle);" />
          <span class="text-sm truncate" style="color: var(--ds-text);">{item.assignee_name}</span>
        </div>
      {:else}
        <span class="text-sm" style="color: var(--ds-text-subtle);">-</span>
      {/if}
    {/if}

  {:else if column.field_identifier === 'milestone'}
    <!-- Milestones (multi) -->
    {@const itemMs = item.milestones || []}
    {@const itemMsIds = itemMs.map(m => m.id)}
    {#if canEdit}
      <MilestoneCombobox
        multiple={true}
        value={itemMsIds}
        workspaceId={item.workspace_id}
        milestones={editorOptions.milestones}
        loading={editorOptions.loading.milestones}
        onOpen={() => collectionEditorOptions.load(item.workspace_id, 'milestones')}
        placeholder={t('pickers.selectMilestones')}
        onSelect={async ({ ids }) => {
          await handleItemUpdate('milestone_ids', ids);
        }}
      >
        {#snippet children()}
          {#if itemMs.length === 0}
            <span class="flex items-center gap-2 text-sm cursor-pointer" style="color: var(--ds-text-subtle);">
              <Target class="w-4 h-4" />
              {t('pickers.selectMilestones')}
            </span>
          {:else}
            <span class="flex items-center gap-1 flex-wrap text-sm cursor-pointer" style="color: var(--ds-text);">
              {#each itemMs as ms (ms.id)}
                <span class="inline-flex items-center gap-1">
                  <ColorDot color={ms.category_color || '#9CA3AF'} />{ms.name}
                </span>
              {/each}
            </span>
          {/if}
        {/snippet}
      </MilestoneCombobox>
    {:else}
      {#if itemMs.length === 0}
        <span class="text-sm" style="color: var(--ds-text-subtle);">-</span>
      {:else}
        <span class="flex items-center gap-1 flex-wrap text-sm" style="color: var(--ds-text);">
          {#each itemMs as ms (ms.id)}
            <span class="inline-flex items-center gap-1">
              <ColorDot color={ms.category_color || '#9CA3AF'} />{ms.name}
            </span>
          {/each}
        </span>
      {/if}
    {/if}

  {:else if column.field_identifier === 'iteration'}
    <!-- Iteration -->
    {@const iteration = [...editorOptions.iterations, ...iterations].find(i => i.id === item.iteration_id) || (item.iteration_name ? { name: item.iteration_name, is_global: false } : null)}
    {#if canEdit}
      <ItemPicker
        value={item.iteration_id}
        items={editorOptions.iterations}
        loading={editorOptions.loading.iterations}
        config={iterationConfig}
        placeholder="Set iteration"
        showUnassigned={true}
        unassignedLabel="No iteration"
        allowClear={true}
        onOpen={() => collectionEditorOptions.load(item.workspace_id, 'iterations')}
        onSelect={async (selected) => {
          const iterationId = selected?.id || null;
          await handleItemUpdate('iteration_id', iterationId);
        }}
      >
        {#snippet children()}
          {#if iteration}
            <span class="flex items-center gap-2 text-sm cursor-pointer" style="color: var(--ds-text);">
              {#if iteration.is_global}
                <Globe class="w-4 h-4" style="color: var(--ds-text-subtle);" />
              {:else}
                <Building2 class="w-4 h-4" style="color: var(--ds-text-subtle);" />
              {/if}
              {iteration.name}
            </span>
          {:else}
            <span class="flex items-center gap-2 text-sm cursor-pointer" style="color: var(--ds-text-subtle);">
              <Calendar class="w-4 h-4" />
              {t('items.selectIteration')}
            </span>
          {/if}
        {/snippet}
      </ItemPicker>
    {:else}
      {#if iteration}
        <span class="flex items-center gap-2 text-sm" style="color: var(--ds-text);">
          {#if iteration.is_global}
            <Globe class="w-4 h-4" style="color: var(--ds-text-subtle);" />
          {:else}
            <Building2 class="w-4 h-4" style="color: var(--ds-text-subtle);" />
          {/if}
          {iteration.name}
        </span>
      {:else}
        <span class="text-sm" style="color: var(--ds-text-subtle);">-</span>
      {/if}
    {/if}

  {:else if column.field_identifier === 'due_date'}
    <!-- Due Date -->
    {#if canEdit}
      <InlineFieldEditor
        {item}
        field="due_date"
        fieldType="date"
        placeholder="Set due date"
        onitemUpdated={(detail) => onitemUpdated?.(detail)}
        onupdateError={(detail) => onupdateError?.(detail)}
      />
    {:else}
      <div class="flex items-center gap-1 text-sm whitespace-nowrap" style="color: var(--ds-text-subtle);">
        <Calendar class="w-4 h-4 flex-shrink-0" />
        {item.due_date ? formatDateOnly(item.due_date) : '-'}
      </div>
    {/if}

  {:else if column.field_identifier === 'created_at'}
    <!-- Created Date (always read-only) -->
    <div class="flex items-center gap-1 text-sm whitespace-nowrap" style="color: var(--ds-text-subtle);">
      <Calendar class="w-4 h-4 flex-shrink-0" />
      {formatDate(item.created_at) || '-'}
    </div>

  {:else if column.field_identifier === 'project'}
    <!-- Project -->
    {@const project = [...editorOptions.projects, ...projects].find(p => p.id === item.project_id) || (item.project_name ? { name: item.project_name } : null)}
    {#if canEdit}
      <ItemPicker
        value={item.project_id}
        items={editorOptions.projects}
        loading={editorOptions.loading.projects}
        config={projectConfig}
        placeholder="Set project"
        showUnassigned={true}
        unassignedLabel="No project"
        allowClear={true}
        onOpen={() => collectionEditorOptions.load(item.workspace_id, 'projects')}
        onSelect={async (selected) => {
          const projectId = selected?.id || null;
          await handleItemUpdate('project_id', projectId);
        }}
      >
        {#snippet children()}
          {#if project}
            <span class="flex items-center gap-2 text-sm cursor-pointer" style="color: var(--ds-text);">
              <FolderKanban class="w-4 h-4" style="color: var(--ds-text-subtle);" />
              {project.name}
            </span>
          {:else}
            <span class="flex items-center gap-2 text-sm cursor-pointer" style="color: var(--ds-text-subtle);">
              <FolderKanban class="w-4 h-4" />
              {t('pickers.selectProject')}
            </span>
          {/if}
        {/snippet}
      </ItemPicker>
    {:else}
      {#if project}
        <span class="flex items-center gap-2 text-sm" style="color: var(--ds-text);">
          <FolderKanban class="w-4 h-4" style="color: var(--ds-text-subtle);" />
          {project.name}
        </span>
      {:else}
        <span class="text-sm" style="color: var(--ds-text-subtle);">-</span>
      {/if}
    {/if}

  {:else}
    <!-- Unknown system field -->
    <span class="text-sm" style="color: var(--ds-text-subtle);">-</span>
  {/if}

{:else if column.field_type === 'custom' && fieldDefinition}
  <!-- Custom Field -->
  {@const customValue = getCustomFieldValue(item, column.field_identifier)}
  <ListCustomFieldCell
    field={fieldDefinition}
    value={customValue}
    {canEdit}
    milestones={editorOptions.loaded.milestones ? editorOptions.milestones : milestones}
    iterations={editorOptions.loaded.iterations ? editorOptions.iterations : iterations}
    users={editorOptions.loaded.users ? editorOptions.users : users}
    editorOptions={editorOptions}
    workspaceId={item.workspace_id}
    itemId={item.id}
    {fieldLinks}
    onFieldLinksChanged={reloadFieldLinks}
    onChange={(newValue) => handleCustomFieldUpdate(column.field_identifier, newValue)}
  />
{:else}
  <!-- Unknown field type or missing definition -->
  <span class="text-sm" style="color: var(--ds-text-subtle);">-</span>
{/if}
