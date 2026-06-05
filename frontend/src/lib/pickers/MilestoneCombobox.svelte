<script>
  import ItemPicker from './ItemPicker.svelte';
  import { api } from '../api.js';
  import { t } from '../stores/i18n.svelte.js';

  // When `multiple` is false (the default), `value` is a single milestone ID
  // (or null) and onSelect emits `{ value, milestone }`.
  // When `multiple` is true, `value` is an array of milestone IDs and
  // onSelect emits `{ ids, milestones }`.
  let {
    value = $bindable(null),
    placeholder = '',
    class: className = '',
    disabled = false,
    workspaceId = null,
    showUnassigned = true,
    unassignedLabel = '',
    children = null,
    multiple = false,
    onSelect = () => {},
    onCancel = () => {}
  } = $props();

  const resolvedPlaceholder = $derived(
    placeholder || (multiple ? t('pickers.selectMilestones') : t('pickers.selectMilestone'))
  );
  const resolvedUnassignedLabel = $derived(unassignedLabel || t('pickers.noMilestone'));

  let milestones = $state([]);
  let loading = $state(false);
  let loadToken = 0;

  // Reload when workspaceId changes. Capture the ID for this request so an
  // earlier global load cannot overwrite a later workspace-scoped result.
  $effect(() => {
    loadMilestones(workspaceId);
  });

  async function loadMilestones(currentWorkspaceId) {
    const token = ++loadToken;
    loading = true;

    try {
      const filters = {};
      if (currentWorkspaceId) {
        filters.workspace_id = currentWorkspaceId;
        filters.include_global = true;
      }

      const response = await api.milestones.getAll(filters);
      if (token !== loadToken) return;
      milestones = response || [];
    } catch (err) {
      if (token !== loadToken) return;
      console.error('Failed to load milestones:', err);
      milestones = [];
    } finally {
      if (token === loadToken) {
        loading = false;
      }
    }
  }

  function handleSelectSingle(milestone) {
    onSelect({
      value: milestone ? milestone.id : null,
      milestone: milestone || null
    });
  }

  function handleSelectMulti(ids) {
    const safe = Array.isArray(ids) ? ids : [];
    const selected = safe
      .map((id) => milestones.find((m) => m.id === id))
      .filter(Boolean);
    onSelect({ ids: safe, milestones: selected });
  }

  const config = {
    icon: {
      type: 'color-dot',
      source: (item) => item.category_color || '#9CA3AF',
      size: 'w-2 h-2'
    },
    primary: { text: (item) => item.name || '' },
    searchFields: ['name', 'description'],
    getValue: (item) => item?.id,
    getLabel: (item) => item?.name ?? ''
  };
</script>

{#if multiple}
  <ItemPicker
    bind:values={value}
    items={milestones}
    {config}
    placeholder={resolvedPlaceholder}
    showUnassigned={false}
    {disabled}
    {loading}
    multiSelect={true}
    class={className}
    {children}
    onSelect={handleSelectMulti}
    onCancel={() => onCancel()}
  />
{:else}
  <ItemPicker
    bind:value
    items={milestones}
    {config}
    placeholder={resolvedPlaceholder}
    {showUnassigned}
    unassignedLabel={resolvedUnassignedLabel}
    {disabled}
    {loading}
    allowClear={true}
    class={className}
    {children}
    onSelect={handleSelectSingle}
    onCancel={() => onCancel()}
  />
{/if}
