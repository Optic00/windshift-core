<script>
  import BasePicker from './BasePicker.svelte';
  import { Building2 } from '@lucide/svelte';
  import { createAsyncLoader } from '../composables';
  import { api } from '../api.js';
  import { onMount } from 'svelte';
  import { t } from '../stores/i18n.svelte.js';

  let {
    value = $bindable(null),
    placeholder = 'Select organisation',
    showUnassigned = false,
    unassignedLabel = 'None',
    disabled = false,
    class: className = '',
    children = null,
    onSelect = () => {},
    onCancel = () => {}
  } = $props();

  const organisations = createAsyncLoader(() => api.customerOrganisations.getAll());
  onMount(() => organisations.load());

  let itemSnippet = null;
  {#if children}
    {#snippet itemSnippet({ item, isSelected })}
      {@render children()}
    {/snippet}
  {/if}

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
    icon={{ type: 'component', source: () => Building2 }}
    itemSnippet={itemSnippet}
    searchFields={['name', 'email', 'description']}
    getValue={(item) => item?.id}
    getLabel={(item) => item?.name || ''}
    onSelect={(item) => onSelect(item)}
    onCancel={() => onCancel()}
  />
</script>