<script>
  import BasePicker from './BasePicker.svelte';
  import { User } from '@lucide/svelte';
  import { createAsyncLoader } from '../composables';
  import { api } from '../api.js';
  import { onMount } from 'svelte';
  import { t } from '../stores/i18n.svelte.js';

  let {
    value = $bindable(null),
    placeholder = 'Select portal customer',
    showUnassigned = false,
    unassignedLabel = 'None',
    disabled = false,
    class: className = '',
    children = null,
    onSelect = () => {},
    onCancel = () => {}
  } = $props();

  const customers = createAsyncLoader(() => api.portalCustomers.getAll());
  onMount(() => customers.load());

  let itemSnippet = null;
  {#if children}
    {#snippet itemSnippet({ item, isSelected })}
      {@render children()}
    {/snippet}
  {/if}

  <BasePicker
    bind:value
    items={customers.data || []}
    loading={customers.loading}
    {placeholder}
    {showUnassigned}
    {unassignedLabel}
    {disabled}
    allowClear={true}
    class={className}
    icon={{ type: 'component', source: () => User }}
    itemSnippet={itemSnippet}
    searchFields={['name', 'email', 'customer_organisation_name']}
    getValue={(item) => item?.id}
    getLabel={(item) => item?.name || ''}
    onSelect={(item) => onSelect(item)}
    onCancel={() => onCancel()}
  />
</script>