<script>
  import { BasePicker } from '.';
  import { api } from '../api.js';
  import { Plus } from '@lucide/svelte';
  import { t } from '../stores/i18n.svelte.js';
  import { errorToast } from '../stores/toasts.svelte.js';

  let {
    workspaceId,
    value = $bindable(/** @type {string[] | string} */ ([])),
    placeholder = '',
    class: className = '',
    disabled = false,
    labels: providedLabels = null,
    loading: providedLoading = false,
    onOpen = null,
    onClose = null,
    onSelect = () => {},
    onCancel = () => {}
  } = $props();

  const resolvedPlaceholder = $derived(placeholder || t('pickers.selectLabels'));

  let loadedLabels = $state([]);
  let createdLabels = $state([]);
  let internalLoading = $state(false);
  let error = $state(null);
  let loadToken = 0;
  const labels = $derived([...(providedLabels ?? loadedLabels), ...createdLabels]);
  const loading = $derived(providedLabels === null ? internalLoading : providedLoading);

  const valueAsNames = $derived.by(() => {
    if (!value) return [];
    if (Array.isArray(value)) return value;
    if (typeof value === 'string' && value.trim()) {
      return value.split(',').map((name) => name.trim()).filter(Boolean);
    }
    return [];
  });

  const valueAsIds = $derived.by(() =>
    valueAsNames
      .map((name) => labels.find((label) => label.name === name)?.id)
      .filter(Boolean)
  );

  $effect(() => {
    if (providedLabels !== null) return;
    void loadLabels();
  });

  async function loadLabels() {
    const token = ++loadToken;
    internalLoading = true;
    error = null;
    createdLabels = [];
    try {
      const response = await api.labels.getAll();
      if (token === loadToken) loadedLabels = response || [];
    } catch (err) {
      if (token !== loadToken) return;
      console.error('Failed to load global labels:', err);
      error = err.message || 'Failed to load labels';
      loadedLabels = [];
    } finally {
      if (token === loadToken) internalLoading = false;
    }
  }

  function handleChange(selectedIds) {
    selectedIds = selectedIds || [];
    const selectedLabels = selectedIds
      .map((id) => labels.find((label) => label.id === id))
      .filter(Boolean);
    const selectedNames = selectedLabels.map((label) => label.name);
    value = selectedNames;
    onSelect({ value: selectedNames, labels: selectedLabels });
  }

  async function handleCreate(searchQuery) {
    const name = searchQuery?.trim();
    const id = Number(workspaceId);
    if (!name || !Number.isFinite(id) || id <= 0) return;
    if (name.includes(',')) {
      error = t('pickers.labelCommaNotAllowed');
      return;
    }

    try {
      const newLabel = await api.labels.create({
        name,
        workspace_id: id,
      });
      createdLabels = [...createdLabels, newLabel];
      const selected = [...valueAsNames, newLabel.name]
        .map((labelName) => labels.find((label) => label.name === labelName))
        .filter(Boolean);
      value = selected.map((label) => label.name);
      onSelect({ value, labels: selected });
    } catch (err) {
      console.error('Failed to create global label:', err);
      errorToast(t('dialogs.alerts.failedToCreateLabel', { error: err.message }));
    }
  }
</script>

<BasePicker
  value={valueAsIds}
  items={labels}
  {loading}
  {error}
  placeholder={resolvedPlaceholder}
  disabled={disabled || !workspaceId}
  class={className}
  multiple={true}
  allowCreate={true}
  onCreate={handleCreate}
  searchFields={['name']}
  getValue={(label) => label?.id}
  getLabel={(label) => label?.name ?? ''}
  onOpen={() => onOpen?.()}
  onClose={() => onClose?.()}
  onChange={handleChange}
  onCancel={() => onCancel?.()}
>
  {#snippet itemSnippet({ item: label })}
    <div class="flex items-center gap-3 flex-1 min-w-0">
      <span
        class="inline-block w-3 h-3 rounded-full flex-shrink-0"
        style="background-color: {label.color || '#3B82F6'};"
        aria-hidden="true"
      ></span>
      <span class="font-medium text-sm" style="color: var(--ds-text);">
        {label.name}
      </span>
    </div>
  {/snippet}

  {#snippet noResultsSnippet({ searchQuery })}
    <div class="p-3 text-sm text-center" style="color: var(--ds-text-subtle);">
      <div class="space-y-2">
        <div>{t('pickers.noLabelsFoundFor', { query: searchQuery })}</div>
        <button
          type="button"
          class="flex items-center gap-2 px-3 py-1 rounded transition-colors mx-auto"
          style="background-color: var(--ds-background-accent-blue-subtlest); color: var(--ds-interactive);"
          onclick={() => handleCreate(searchQuery)}
        >
          <Plus class="w-4 h-4" />
          {t('pickers.createItem', { value: searchQuery })}
        </button>
      </div>
    </div>
  {/snippet}
</BasePicker>
