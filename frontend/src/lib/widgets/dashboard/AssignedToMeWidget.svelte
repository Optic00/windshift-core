<script>
  import { CheckSquare } from '@lucide/svelte';
  import { authStore } from '../../stores';
  import { api } from '../../api.js';
  import DashboardItemRow from './DashboardItemRow.svelte';
  import { normalizeTaskResponse, openTask } from './taskWidgetState.js';

  let tasks = $state([]);
  let loading = $state(false);
  let errored = $state(false);
  let lastUserId = null;
  let version = 0;

  const currentUserId = $derived($authStore?.currentUser?.id ?? null);

  $effect(() => {
    if (currentUserId && currentUserId !== lastUserId) {
      lastUserId = currentUserId;
      load();
    } else if (!currentUserId && lastUserId !== null) {
      lastUserId = null;
      tasks = [];
    }
  });

  async function load() {
    const v = ++version;
    loading = true;
    errored = false;
    try {
      const response = await api.items.getAll({
        ql: `assignee_id = ${currentUserId} AND status_completed = false`,
        limit: 30,
        order_by: 'updated_at',
      });
      if (v !== version) return;
      tasks = normalizeTaskResponse(response);
    } catch (err) {
      if (v !== version) return;
      console.error('Failed to load assigned items:', err);
      errored = true;
      tasks = [];
    } finally {
      if (v === version) loading = false;
    }
  }
</script>

{#if loading && tasks.length === 0}
  <div class="space-y-2 animate-pulse">
    {#each Array(3) as _}
      <div class="h-11 rounded" style="background-color: var(--ds-background-neutral);"></div>
    {/each}
  </div>
{:else if errored}
  <div class="flex flex-col items-center text-center py-6" style="color: var(--ds-text-subtle);">
    <CheckSquare class="w-6 h-6 mb-2 opacity-60" />
    <p class="text-sm">Couldn't load your assigned items</p>
  </div>
{:else if tasks.length === 0}
  <div class="flex flex-col items-center text-center py-6" style="color: var(--ds-text-subtle);">
    <CheckSquare class="w-6 h-6 mb-2 opacity-60" />
    <p class="text-sm">Nothing assigned to you right now</p>
  </div>
{:else}
  <ul class="flex flex-col gap-1.5">
    {#each tasks as task (task.id)}
      <li>
        <DashboardItemRow
          title={task.title}
          itemKey={`${task.workspace_key}-${task.workspace_item_number}`}
          statusName={task.status_name}
          statusColor={task.status_color}
          priorityName={task.priority_name}
          priorityColor={task.priority_color}
          dueDate={task.dueDate}
          onclick={() => openTask(task)}
        />
      </li>
    {/each}
  </ul>
{/if}