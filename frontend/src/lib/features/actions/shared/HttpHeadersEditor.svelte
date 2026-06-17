<script>
  import { Plus, Trash2 } from '@lucide/svelte';
  import { t } from '../../../stores/i18n.svelte.js';

  let {
    // Header map as stored in the node config: { "Header-Name": "value" }.
    headers = {},
    // Identity of the owning node; used to re-seed the row state when a
    // different node is selected (the parent reuses this component instance).
    nodeId = '',
    onchange = () => {},
  } = $props();

  function objectToRows(obj) {
    return Object.entries(obj || {}).map(([key, value]) => ({ key, value: String(value ?? '') }));
  }

  // Internal row state so a half-typed or temporarily-empty key doesn't drop
  // the row out of the map mid-edit. We rebuild the object on every change.
  let rows = $state(objectToRows(headers));
  let seededFor = nodeId;

  $effect(() => {
    if (nodeId !== seededFor) {
      seededFor = nodeId;
      rows = objectToRows(headers);
    }
  });

  function emit() {
    const obj = {};
    for (const row of rows) {
      const key = row.key.trim();
      if (key) obj[key] = row.value;
    }
    onchange(obj);
  }

  function updateRow(index, field, value) {
    rows[index] = { ...rows[index], [field]: value };
    emit();
  }

  function addRow() {
    rows = [...rows, { key: '', value: '' }];
  }

  function removeRow(index) {
    rows = rows.filter((_, i) => i !== index);
    emit();
  }
</script>

<div class="space-y-2">
  {#each rows as row, index}
    <div class="flex items-center gap-2">
      <input
        type="text"
        class="flex-1 px-2 py-1.5 border rounded text-xs config-input"
        value={row.key}
        oninput={(e) => updateRow(index, 'key', e.currentTarget.value)}
        placeholder={t('actions.config.headerName')}
        data-testid={`http-header-key-${index}`}
      />
      <input
        type="text"
        class="flex-1 px-2 py-1.5 border rounded text-xs config-input"
        value={row.value}
        oninput={(e) => updateRow(index, 'value', e.currentTarget.value)}
        placeholder={t('actions.config.headerValue')}
        data-testid={`http-header-value-${index}`}
      />
      <button
        onclick={() => removeRow(index)}
        class="p-1 hover-danger rounded transition-colors flex-shrink-0" style="color: var(--ds-icon-danger);"
        title={t('actions.config.addHeader')}
        data-testid={`http-header-remove-${index}`}
      >
        <Trash2 size={14} />
      </button>
    </div>
  {/each}

  <button
    onclick={addRow}
    class="w-full px-3 py-2 text-sm border border-dashed rounded-md flex items-center justify-center gap-2 add-header-btn"
    data-testid="http-header-add"
  >
    <Plus size={14} />
    {t('actions.config.addHeader')}
  </button>
</div>

<style>
  .config-input {
    background-color: var(--ds-surface);
    border-color: var(--ds-border);
    color: var(--ds-text);
  }

  .config-input:focus {
    border-color: var(--ds-interactive);
    outline: none;
  }

  .add-header-btn {
    color: var(--ds-text-subtle);
    border-color: var(--ds-border);
    background-color: transparent;
    cursor: pointer;
    transition: all 0.15s ease;
  }

  .add-header-btn:hover {
    background-color: var(--ds-background-neutral-hovered);
    border-color: var(--ds-interactive);
    color: var(--ds-interactive);
  }
</style>
