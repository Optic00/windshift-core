<script>
  import { Milestone } from '@lucide/svelte';
  import { actionFlowStore } from '../../../stores/actionFlowStore.svelte.js';
  import GenericActionNode from '../shared/GenericActionNode.svelte';

  let { data = {}, selected = false } = $props();
</script>

<GenericActionNode {data} {selected} flowStore={data.flowStore || actionFlowStore} icon={Milestone} title="Create milestone" accentColor="green">
  {#snippet body()}
    {#if data.config?.upsert_key_template}
      <div class="cm-info">
        <span class="cm-label">key</span>
        <span class="cm-template">{data.config.upsert_key_template}</span>
      </div>
      {#if data.config?.name_template}
        <div class="cm-info">
          <span class="cm-label">name</span>
          <span class="cm-template">{data.config.name_template}</span>
        </div>
      {/if}
    {:else}
      <div class="placeholder">Configure milestone upsert</div>
    {/if}
  {/snippet}
</GenericActionNode>

<style>
  .cm-info {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
  }
  .cm-label {
    color: var(--ds-text-subtle);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .cm-template {
    color: var(--ds-text);
    font-family: monospace;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 140px;
  }
  .placeholder { color: var(--ds-text-subtle); font-size: 12px; font-style: italic; }
</style>
