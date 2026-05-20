<script>
  import { UsersRound } from '@lucide/svelte';
  import { actionFlowStore } from '../../../stores/actionFlowStore.svelte.js';
  import GenericActionNode from '../shared/GenericActionNode.svelte';

  let { data = {}, selected = false } = $props();
</script>

<GenericActionNode
  {data}
  {selected}
  flowStore={actionFlowStore}
  icon={UsersRound}
  title="Round-robin assign"
  accentColor="magenta"
>
  {#snippet body()}
    {#if data.config?.team_id}
      <div class="assignment-info">
        <span class="team-name">{data.config.team_name || `Team #${data.config.team_id}`}</span>
      </div>
    {:else}
      <div class="placeholder">Select team</div>
    {/if}
  {/snippet}
</GenericActionNode>

<style>
  .assignment-info {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
  }

  .team-name {
    color: var(--ds-text);
    font-weight: 500;
  }
</style>
