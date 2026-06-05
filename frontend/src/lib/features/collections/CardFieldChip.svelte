<script>
  import { Clock, CornerLeftUp } from '@lucide/svelte';
  import { formatDate, formatDateShort, formatStatusAge } from '../../utils/dateFormatter.js';
  import { resolveOptionLabel } from '../../utils/optionUtils.js';
  import { durationToString } from '../../utils/timeUtils.js';

  // Renders the chip(s) for a single configured board card field. Centralising
  // this here keeps the board configuration surface (CARD_SELECTABLE_FIELDS) and
  // the on-card rendering from drifting apart — every selectable field should
  // have a branch below.
  let {
    cardField,
    item,
    priorities = [],
    statuses = [],
    iterations = [],
    projects = [],
    labels = [],
    customFieldDefinitions = [],
  } = $props();

  // Shared chip presentation.
  const CHIP_CLASS = 'inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px]';
  const CHIP_STYLE = 'background: var(--ds-surface); color: var(--ds-text-subtle);';

  let customFieldId = $derived(
    cardField.field_type === 'custom'
      ? parseInt(cardField.field_identifier.replace('custom_field_', ''))
      : null
  );
  let customFieldDef = $derived(
    customFieldId != null ? customFieldDefinitions.find(d => d.id === customFieldId) : null
  );
  let customFieldValue = $derived(
    customFieldId != null
      ? (item.custom_field_values?.[customFieldId] ?? item.custom_field_values?.[String(customFieldId)])
      : null
  );
</script>

{#if cardField.field_type === 'system'}
  {#if cardField.field_identifier === 'priority' && item.priority_id}
    {@const prio = priorities.find(p => p.id === item.priority_id)}
    {#if prio}
      <span class="{CHIP_CLASS} font-medium" style="background: {prio.color}20; color: {prio.color};">
        {prio.name}
      </span>
    {/if}
  {:else if cardField.field_identifier === 'due_date' && item.due_date}
    <span class={CHIP_CLASS} style={CHIP_STYLE}>
      {formatDateShort(item.due_date)}
    </span>
  {:else if cardField.field_identifier === 'start_date' && item.start_date}
    <span class={CHIP_CLASS} style={CHIP_STYLE} title="Start date">
      Start: {formatDateShort(item.start_date)}
    </span>
  {:else if cardField.field_identifier === 'end_date' && item.end_date}
    <span class={CHIP_CLASS} style={CHIP_STYLE} title="End date">
      End: {formatDateShort(item.end_date)}
    </span>
  {:else if cardField.field_identifier === 'story_points' && item.story_points != null}
    <span class={CHIP_CLASS} style={CHIP_STYLE} title="Story points">
      {item.story_points} pts
    </span>
  {:else if cardField.field_identifier === 'estimate' && item.estimate_minutes != null}
    <span class={CHIP_CLASS} style={CHIP_STYLE} title="Estimate">
      {durationToString(item.estimate_minutes, { withDays: true })}
    </span>
  {:else if cardField.field_identifier === 'milestone' && (item.milestones?.length ?? 0) > 0}
    {#each item.milestones as ms (ms.id)}
      <span class={CHIP_CLASS} style={CHIP_STYLE}>
        <span class="w-2 h-2 rounded-full flex-shrink-0" style="background-color: {ms.category_color || '#6b7280'};"></span>
        {ms.name}
      </span>
    {/each}
  {:else if cardField.field_identifier === 'iteration' && item.iteration_id}
    {@const iter = iterations.find(i => i.id === item.iteration_id)}
    {#if iter}
      <span class={CHIP_CLASS} style={CHIP_STYLE}>
        {iter.name}
      </span>
    {/if}
  {:else if cardField.field_identifier === 'labels' && item.label_ids?.length > 0}
    {#each item.label_ids.slice(0, 3) as labelId}
      {@const lbl = labels.find(l => l.id === labelId)}
      {#if lbl}
        <span class="{CHIP_CLASS} text-white font-medium" style="background-color: {lbl.color || '#6b7280'};">
          {lbl.name}
        </span>
      {/if}
    {/each}
    {#if item.label_ids.length > 3}
      <span class={CHIP_CLASS} style={CHIP_STYLE}>
        +{item.label_ids.length - 3}
      </span>
    {/if}
  {:else if cardField.field_identifier === 'status' && item.status_id}
    {@const st = statuses.find(s => s.id === item.status_id)}
    {#if st}
      <span class={CHIP_CLASS} style={CHIP_STYLE}>
        <span class="w-2 h-2 rounded-full flex-shrink-0" style="background-color: {st.color || st.category_color || '#6b7280'};"></span>
        {st.name}
      </span>
    {/if}
  {:else if cardField.field_identifier === 'created_at' && item.created_at}
    <span class={CHIP_CLASS} style={CHIP_STYLE}>
      {formatDateShort(item.created_at)}
    </span>
  {:else if cardField.field_identifier === 'project' && item.project_id}
    {@const proj = projects.find(p => p.id === item.project_id)}
    {#if proj}
      <span class={CHIP_CLASS} style={CHIP_STYLE}>
        {proj.name}
      </span>
    {/if}
  {:else if cardField.field_identifier === 'parent' && item.parent_id}
    {@const parentKey = item.parent_workspace_item_number != null && item.workspace_key
      ? `${item.workspace_key}-${item.parent_workspace_item_number}`
      : null}
    <span
      class="{CHIP_CLASS} max-w-[12rem]"
      style={CHIP_STYLE}
      title={item.parent_title || parentKey || 'Parent'}
    >
      <CornerLeftUp class="w-3 h-3 flex-shrink-0" />
      {#if parentKey}
        <span class="font-mono flex-shrink-0">{parentKey}</span>
      {/if}
      {#if item.parent_title}
        <span class="truncate">{item.parent_title}</span>
      {/if}
    </span>
  {:else if cardField.field_identifier === 'time_in_status' && item.status_since}
    {@const age = formatStatusAge(item.status_since)}
    {#if age}
      <span
        class={CHIP_CLASS}
        style={CHIP_STYLE}
        title="In current status since {formatDate(item.status_since)}"
      >
        <Clock class="w-3 h-3 flex-shrink-0" />
        {age}
      </span>
    {/if}
  {/if}
{:else if cardField.field_type === 'custom'}
  {#if customFieldDef && customFieldValue}
    <span class={CHIP_CLASS} style={CHIP_STYLE}>
      {#if customFieldDef.field_type === 'date'}
        {formatDateShort(customFieldValue)}
      {:else if (customFieldDef.field_type === 'select' || customFieldDef.field_type === 'multiselect') && customFieldDef.options}
        {resolveOptionLabel(customFieldDef.options, customFieldValue) || customFieldValue}
      {:else}
        {customFieldValue}
      {/if}
    </span>
  {/if}
{/if}
