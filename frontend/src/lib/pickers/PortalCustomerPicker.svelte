<script>
  import BasePicker from './BasePicker.svelte';
  import { User } from '@lucide/svelte';
  import { createAsyncLoader } from '../composables';
  import { api } from '../api.js';
  import { onMount } from 'svelte';

  let {
    value = $bindable(null),
    placeholder = 'Select portal customer',
    showUnassigned = false,
    unassignedLabel = 'None',
    disabled = false,
    class: className = '',
    onSelect = () => {},
    onCancel = () => {}
  } = $props();

  const customers = createAsyncLoader(() => api.portalCustomers.getAll());
  onMount(() => customers.load());
</script>

{#snippet customerRow({ item })}
  <User class="w-4 h-4 flex-shrink-0" />
  <div class="flex flex-col min-w-0">
    <span class="font-medium truncate">{item?.name || ''}</span>
    {#if item?.email}
      <span class="text-xs truncate" style="color: var(--ds-text-subtle);">{item.email}</span>
    {/if}
  </div>
{/snippet}

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
  itemSnippet={customerRow}
  searchFields={['name', 'email', 'customer_organisation_name']}
  getValue={(item) => item?.id}
  getLabel={(item) => item?.name || ''}
  onSelect={(item) => onSelect(item)}
  onCancel={() => onCancel()}
/>
