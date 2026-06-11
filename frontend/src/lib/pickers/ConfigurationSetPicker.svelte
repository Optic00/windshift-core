<script>
  import BasePicker from './BasePicker.svelte';
  import { Settings } from '@lucide/svelte';
  import { t } from '../stores/i18n.svelte.js';

  let {
    value = $bindable(null),
    items = [],
    placeholder = '',
    disabled = false,
    class: className = '',
    onSelect = () => {},
    onCancel = () => {}
  } = $props();

  const resolvedPlaceholder = $derived(placeholder || t('pickers.defaultConfiguration'));

  <BasePicker
    bind:value
    {items}
    {placeholder}
    showUnassigned={true}
    unassignedLabel={t('pickers.defaultConfiguration')}
    {disabled}
    allowClear={true}
    class={className}
    icon={{ type: 'component', source: () => Settings }}
    searchFields={['name', 'description']}
    getValue={(item) => item?.id}
    getLabel={(item) => item?.name || ''}
    onSelect={(item) => onSelect(item)}
    onCancel={() => onCancel()}
  />
</script>