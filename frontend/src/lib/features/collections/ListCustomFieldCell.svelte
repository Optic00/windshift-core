<script>
  import ItemPicker from '../../pickers/ItemPicker.svelte';
  import UserPicker from '../../pickers/UserPicker.svelte';
  import CustomFieldRenderer from '../items/CustomFieldRenderer.svelte';
  import { collectionEditorOptions } from '../../stores/collectionEditorOptions.svelte.js';
  import ColorDot from '../../components/ColorDot.svelte';
  import { Calendar, User, Target, Globe, Building2 } from '@lucide/svelte';
  import {
    milestonePickerConfig as milestoneConfig,
    iterationPickerConfig as iterationConfig,
  } from '../../pickers/pickerConfigs.js';

  let {
    field,
    value = null,
    canEdit = false,
    milestones = [],
    iterations = [],
    users = [],
    editorOptions = null,
    workspaceId = null,
    itemId = null,
    fieldLinks = [],
    onFieldLinksChanged = null,
    onChange = (_value) => {}
  } = $props();

</script>

{#if field.field_type === 'milestone'}
  {@const milestone = value ? [...(editorOptions?.milestones ?? []), ...milestones].find(m => m.id === parseInt(value)) : null}
  {#if canEdit}
    <ItemPicker
      {value}
      items={editorOptions?.milestones ?? milestones}
      config={milestoneConfig}
      placeholder={field.name}
      showUnassigned={true}
      unassignedLabel="No {field.name.toLowerCase()}"
      allowClear={true}
      loading={editorOptions?.loading?.milestones ?? false}
      onOpen={() => editorOptions && collectionEditorOptions.load(workspaceId, 'milestones')}
      onSelect={(item) => onChange(item?.id || null)}
    >
      {#snippet children()}
        {#if milestone}
          <span class="flex items-center gap-2 text-sm cursor-pointer" style="color: var(--ds-text);">
            <ColorDot color={milestone.category_color || '#9CA3AF'} />
            {milestone.name}
          </span>
        {:else}
          <span class="flex items-center gap-2 text-sm cursor-pointer" style="color: var(--ds-text-subtle);">
            <Target class="w-4 h-4" />
            {field.name}
          </span>
        {/if}
      {/snippet}
    </ItemPicker>
  {:else}
    {#if milestone}
      <span class="flex items-center gap-2 text-sm" style="color: var(--ds-text);">
        <ColorDot color={milestone.category_color || '#9CA3AF'} />
        {milestone.name}
      </span>
    {:else}
      <span class="text-sm" style="color: var(--ds-text-subtle);">-</span>
    {/if}
  {/if}

{:else if field.field_type === 'iteration'}
  {@const iteration = value ? [...(editorOptions?.iterations ?? []), ...iterations].find(i => i.id === parseInt(value)) : null}
  {#if canEdit}
    <ItemPicker
      {value}
      items={editorOptions?.iterations ?? iterations}
      config={iterationConfig}
      placeholder={field.name}
      showUnassigned={true}
      unassignedLabel="No {field.name.toLowerCase()}"
      allowClear={true}
      loading={editorOptions?.loading?.iterations ?? false}
      onOpen={() => editorOptions && collectionEditorOptions.load(workspaceId, 'iterations')}
      onSelect={(item) => onChange(item?.id || null)}
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
            {field.name}
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

{:else if field.field_type === 'user'}
  {@const userValue = value && typeof value === 'object' ? value.id : value}
  {@const assignee = userValue ? [...(editorOptions?.users ?? []), ...users].find(u => u.id === parseInt(userValue)) : null}
  {#if canEdit}
    <UserPicker
      value={userValue}
      placeholder={field.name}
      showUnassigned={true}
      users={editorOptions?.users ?? users}
      loading={editorOptions?.loading?.users ?? false}
      onOpen={() => editorOptions && collectionEditorOptions.load(workspaceId, 'users')}
      onSelect={(selectedUser) => {
        onChange(selectedUser ? {
          id: selectedUser.id,
          name: `${selectedUser.first_name} ${selectedUser.last_name}`.trim() || selectedUser.username
        } : null);
      }}
    >
      {#snippet children()}
        {#if assignee}
          <div class="flex items-center gap-2 cursor-pointer">
            <div class="w-5 h-5 rounded-full bg-blue-500 flex items-center justify-center text-white text-[10px] font-medium">
              {(assignee.first_name?.[0] || '') + (assignee.last_name?.[0] || '') || assignee.username?.[0]?.toUpperCase() || '?'}
            </div>
            <span class="text-sm truncate" style="color: var(--ds-text);">
              {assignee.first_name} {assignee.last_name}
            </span>
          </div>
        {:else if value && typeof value === 'object' && value.name}
          <div class="flex items-center gap-2 cursor-pointer">
            <div class="w-5 h-5 rounded-full bg-blue-500 flex items-center justify-center text-white text-[10px] font-medium">
              {value.name.split(' ').map(n => n[0]).join('').toUpperCase().slice(0, 2)}
            </div>
            <span class="text-sm truncate" style="color: var(--ds-text);">
              {value.name}
            </span>
          </div>
        {:else}
          <span class="flex items-center gap-2 text-sm cursor-pointer" style="color: var(--ds-text-subtle);">
            <User class="w-4 h-4" />
            {field.name}
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
    {:else if value && typeof value === 'object' && value.name}
      <div class="flex items-center gap-2">
        <div class="w-5 h-5 rounded-full bg-blue-500 flex items-center justify-center text-white text-[10px] font-medium">
          {value.name.split(' ').map(n => n[0]).join('').toUpperCase().slice(0, 2)}
        </div>
        <span class="text-sm truncate" style="color: var(--ds-text);">
          {value.name}
        </span>
      </div>
    {:else}
      <span class="text-sm" style="color: var(--ds-text-subtle);">-</span>
    {/if}
  {/if}

{:else}
  <!-- Non-picker types: delegate to CustomFieldRenderer -->
  <CustomFieldRenderer
    {field}
    {value}
    readonly={!canEdit}
    disabled={!canEdit}
    autoOpenPickers={false}
    {milestones}
    {iterations}
    users={editorOptions?.loaded?.users ? editorOptions.users : users}
    optionData={editorOptions ?? {}}
    optionLoading={editorOptions?.loading ?? {}}
    onRequestOptions={(field) => editorOptions && collectionEditorOptions.load(workspaceId, field)}
    loadAssetOptions={(assetSetId, cqlQuery, search) => collectionEditorOptions.loadAssets(workspaceId, assetSetId, cqlQuery, search)}
    {itemId}
    {fieldLinks}
    {onFieldLinksChanged}
    onChange={(val) => onChange(val)}
  />
{/if}
