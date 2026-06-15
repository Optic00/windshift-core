<script>
  import BasePicker from './BasePicker.svelte';
  import { Building2 } from '@lucide/svelte';
  import { createAsyncLoader } from '../composables';
  import { api } from '../api.js';
  import { onMount } from 'svelte';

  let {
    value = $bindable(null),
    placeholder = 'Select organisation',
    showUnassigned = false,
    unassignedLabel = 'None',
    disabled = false,
    class: className = '',
    onSelect = () => {},
    onCancel = () => {}
  } = $props();

  const organisations = createAsyncLoader(() => api.customerOrganisations.getAll());
  onMount(() => organisations.load());
</script>

{#snippet organisationRow({ item })}
  <Building2 class="w-4 h-4 flex-shrink-0" />
  <div class="flex flex-col min-w-0">
    <span class="font-medium truncate">{item?.name || ''}</span>
    {#if item?.email || item?.description}
      <span class="text-xs truncate" style="color: var(--ds-text-subtle);">{item.email || item.description}</span>
    {/if}
  </div>
{/snippet}

<BasePicker
  bind:value
  items={organisations.data || []}
  loading={organisations.loading}
  {placeholder}
  {showUnassigned}
  {unassignedLabel}
  {disabled}
  allowClear={true}
  class={className}
  itemSnippet={organisationRow}
  searchFields={['name', 'email', 'description']}
  getValue={(item) => item?.id}
  getLabel={(item) => item?.name || ''}
  onSelect={(item) => onSelect(item)}
  onCancel={() => onCancel()}
/>
