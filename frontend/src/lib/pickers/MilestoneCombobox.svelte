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
    milestones: providedMilestones = null,
    loading: providedLoading = false,
    showUnassigned = true,
    unassignedLabel = '',
    children = null,
    multiple = false,
    onOpen = null,
    onSelect = () => {},
    onCancel = () => {}
  } = $props();

  const resolvedPlaceholder = $derived(
    placeholder || (multiple ? t('pickers.selectMilestones') : t('pickers.selectMilestone'))
  );
  const resolvedUnassignedLabel = $derived(unassignedLabel || t('pickers.noMilestone'));

  let loadedMilestones = $state([]);
  let internalLoading = $state(false);
  let loadToken = 0;
  const milestones = $derived(providedMilestones ?? loadedMilestones);
  const loading = $derived(providedMilestones === null ? internalLoading : providedLoading);

  // Terminal milestone lifecycle statuses are hidden from the list by default
  // so completed/cancelled milestones no longer look identical to active ones
  // (WI-448). They can be surfaced again via the "Show completed" footer toggle.
  const TERMINAL_STATUSES = new Set(['completed', 'cancelled']);

  // Whether the user has opted to surface terminal milestones.
  let showCompleted = $state(false);

  // Reload when workspaceId changes. Capture the ID for this request so an
  // earlier global load cannot overwrite a later workspace-scoped result.
  $effect(() => {
    if (providedMilestones === null) loadMilestones(workspaceId);
  });

  async function loadMilestones(currentWorkspaceId) {
    const token = ++loadToken;
    internalLoading = true;

    try {
      const filters = {};
      if (currentWorkspaceId) {
        filters.workspace_id = currentWorkspaceId;
        filters.include_global = true;
      }

      const response = await api.milestones.getAll(filters);
      if (token !== loadToken) return;
      loadedMilestones = response || [];
    } catch (err) {
      if (token !== loadToken || err?.name === 'AbortError') return;
      console.error('Failed to load milestones:', err);
      loadedMilestones = [];
    } finally {
      if (token === loadToken) {
        internalLoading = false;
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

  // Terminal milestones hidden by default; surfaced via the footer toggle.
  const hasCompletedMilestones = $derived(
    milestones.some((m) => TERMINAL_STATUSES.has(m.status))
  );

  // The currently-selected value(s) must always stay visible so the trigger
  // reflects the real selection accurately, even when it would otherwise be
  // filtered out as a terminal milestone.
  const selectedIds = $derived.by(() => {
    const ids = new Set();
    if (multiple) {
      const arr = Array.isArray(value) ? value : [];
      for (const id of arr) ids.add(id);
    } else if (value != null) {
      ids.add(value);
    }
    return ids;
  });

  const visibleMilestones = $derived.by(() => {
    if (showCompleted) return milestones;
    return milestones.filter(
      (m) => !TERMINAL_STATUSES.has(m.status) || selectedIds.has(m.id)
    );
  });

  // Status-specific badge so a cancelled milestone is never mislabelled as
  // "Completed". Cancelled uses the danger tone, completed the success tone,
  // matching the milestone detail page's lozenge colours. Backgrounds are a
  // subtle tint derived from the well-defined text tokens (there is no
  // `--ds-background-success`/`-danger` semantic token in the theme).
  function terminalBadge(milestone) {
    if (milestone.status === 'completed') {
      return {
        text: t('milestones.status.completed'),
        textColor: 'var(--ds-text-success)',
        bgColor: 'color-mix(in srgb, var(--ds-text-success) 15%, transparent)'
      };
    }
    if (milestone.status === 'cancelled') {
      return {
        text: t('milestones.status.cancelled'),
        textColor: 'var(--ds-text-danger)',
        bgColor: 'color-mix(in srgb, var(--ds-text-danger) 15%, transparent)'
      };
    }
    return null;
  }

  const config = {
    icon: {
      type: 'color-dot',
      source: (item) => item.category_color || '#9CA3AF',
      size: 'w-2 h-2'
    },
    primary: { text: (item) => item.name || '' },
    badges: [
      {
        text: (item) => terminalBadge(item)?.text ?? '',
        textColor: (item) => terminalBadge(item)?.textColor ?? 'var(--ds-text-subtle)',
        bgColor: (item) => terminalBadge(item)?.bgColor ?? 'var(--ds-background-neutral)'
      }
    ],
    searchFields: ['name', 'description'],
    getValue: (item) => item?.id,
    getLabel: (item) => item?.name ?? ''
  };
</script>

{#if multiple}
  <ItemPicker
    bind:values={value}
    items={visibleMilestones}
    {config}
    placeholder={resolvedPlaceholder}
    showUnassigned={false}
    {disabled}
    {loading}
    multiSelect={true}
    class={className}
    {children}
    keepOpenOnFooterTab={hasCompletedMilestones}
    onSelect={handleSelectMulti}
    onOpen={() => onOpen?.()}
    onCancel={() => onCancel()}
  >
    {#snippet footer()}
      {#if hasCompletedMilestones}
        <label
          class="flex items-center gap-2 px-4 py-2.5 cursor-pointer text-sm select-none"
          style="color: var(--ds-text);"
          data-testid="milestone-show-completed-toggle"
        >
          <input type="checkbox" bind:checked={showCompleted} class="accent-[var(--ds-interactive)]" />
          <span>{t('pickers.showCompletedMilestones')}</span>
        </label>
      {/if}
    {/snippet}
  </ItemPicker>
{:else}
  <ItemPicker
    bind:value
    items={visibleMilestones}
    {config}
    placeholder={resolvedPlaceholder}
    {showUnassigned}
    unassignedLabel={resolvedUnassignedLabel}
    {disabled}
    {loading}
    allowClear={true}
    class={className}
    {children}
    keepOpenOnFooterTab={hasCompletedMilestones}
    onSelect={handleSelectSingle}
    onOpen={() => onOpen?.()}
    onCancel={() => onCancel()}
  >
    {#snippet footer()}
      {#if hasCompletedMilestones}
        <label
          class="flex items-center gap-2 px-4 py-2.5 cursor-pointer text-sm select-none"
          style="color: var(--ds-text);"
          data-testid="milestone-show-completed-toggle"
        >
          <input type="checkbox" bind:checked={showCompleted} class="accent-[var(--ds-interactive)]" />
          <span>{t('pickers.showCompletedMilestones')}</span>
        </label>
      {/if}
    {/snippet}
  </ItemPicker>
{/if}
