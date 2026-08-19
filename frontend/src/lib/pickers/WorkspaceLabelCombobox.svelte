<script>
  import { BasePicker } from '.';
  import { api } from '../api.js';
  import { t } from '../stores/i18n.svelte.js';
  import { errorToast } from '../stores/toasts.svelte.js';
  import { parseLabelValue, labelIdsForNames } from './labelComboboxUtils.js';
  import LabelItemRow from './LabelItemRow.svelte';
  import LabelCreateRow from './LabelCreateRow.svelte';

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

  const valueAsNames = $derived.by(() => parseLabelValue(value));

  const valueAsIds = $derived.by(() => labelIdsForNames(valueAsNames, labels));

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
    <LabelItemRow {label} />
  {/snippet}

  {#snippet noResultsSnippet({ searchQuery })}
    <LabelCreateRow searchQuery={searchQuery} oncreate={handleCreate} />
  {/snippet}
</BasePicker>
