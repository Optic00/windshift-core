<script>
  import { BasePicker } from '.';
  import { onMount } from 'svelte';
  import { api } from '../api.js';
  import { authStore } from '../stores';
  import { t } from '../stores/i18n.svelte.js';
  import { errorToast } from '../stores/toasts.svelte.js';
  import { parseLabelValue, labelIdsForNames } from './labelComboboxUtils.js';
  import LabelItemRow from './LabelItemRow.svelte';
  import LabelCreateRow from './LabelCreateRow.svelte';

  // userId semantics:
  //   undefined  → unified mode: load mine ∪ shared; inline-create makes a
  //                personal label owned by the current user.
  //   null       → legacy custom-field mode: load shared (global) labels only;
  //                inline-create makes a shared label.
  //   <number>   → that user's labels (mine ∪ shared); inline-create assigns
  //                user_id = <number>.
  let {
    value = $bindable(/** @type {string[] | string} */ ([])),
    placeholder = '',
    class: className = '',
    disabled = false,
    userId = undefined,
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
  let pickerRef = $state(null);
  const labels = $derived([...(providedLabels ?? loadedLabels), ...createdLabels]);
  const loading = $derived(providedLabels === null ? internalLoading : providedLoading);

  // Convert value (array of names or comma-separated string) to array of names
  const valueAsNames = $derived.by(() => parseLabelValue(value));

  // Map label names to label IDs for the picker
  const valueAsIds = $derived.by(() => labelIdsForNames(valueAsNames, labels));

  onMount(async () => {
    if (providedLabels === null) await loadLabels();
  });

  async function loadLabels() {
    internalLoading = true;
    error = null;
    try {
      const response = await api.personalLabels.getAll(userId);
      loadedLabels = response || [];
    } catch (err) {
      console.error('Failed to load personal labels:', err);
      error = err.message || 'Failed to load labels';
      loadedLabels = [];
    } finally {
      internalLoading = false;
    }
  }

  function handleChange(selectedIds) {
    // Convert IDs back to names
    selectedIds = selectedIds || [];
    const selectedNames = selectedIds
      .map(id => labels.find(l => l.id === id)?.name)
      .filter(Boolean);

    value = selectedNames;

    const selectedLabels = selectedIds
      .map(id => labels.find(l => l.id === id))
      .filter(Boolean);

    onSelect({
      value: selectedNames,
      labels: selectedLabels
    });
  }

  function handleCancel() {
    onCancel();
  }

  async function handleCreate(searchQuery) {
    if (!searchQuery?.trim()) return;

    if (searchQuery.includes(',')) {
      error = t('pickers.labelCommaNotAllowed');
      return;
    }

    // Resolve who owns the new label:
    //   unified mode (userId === undefined): create as personal for me
    //   legacy/null: create as shared (user_id null)
    //   explicit id: that user
    const createUserId = userId === undefined
      ? (authStore.currentUser?.id ?? null)
      : userId;

    try {
      const newLabel = await api.personalLabels.create({
        name: searchQuery.trim(),
        user_id: createUserId
      });

      // Add to local labels array
      createdLabels = [...createdLabels, newLabel];

      // Add the newly created label to selection
      const newValue = [...valueAsNames, newLabel.name];
      value = newValue;

      const selected = newValue
        .map((name) => labels.find((l) => l.name === name))
        .filter(Boolean);

      onSelect({ value: newValue, labels: selected });
    } catch (err) {
      console.error('Failed to create label:', err);
      errorToast(t('dialogs.alerts.failedToCreateLabel', { error: err.message }));
    }
  }
</script>

<BasePicker
  bind:this={pickerRef}
  value={valueAsIds}
  items={labels}
  {loading}
  {error}
  placeholder={resolvedPlaceholder}
  {disabled}
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
  onCancel={handleCancel}
>
  {#snippet itemSnippet({ item: label })}
    <LabelItemRow {label} />
  {/snippet}

  {#snippet noResultsSnippet({ searchQuery })}
    <LabelCreateRow searchQuery={searchQuery} oncreate={handleCreate} />
  {/snippet}
</BasePicker>
