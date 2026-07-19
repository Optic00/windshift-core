<script>
  import { ListChecks } from '@lucide/svelte';
  import { authStore, workspacesStore } from '../../stores';
  import { api } from '../../api.js';
  import DashboardItemRow from './DashboardItemRow.svelte';
  import { completedSinceCutoff, normalizeTaskResponse, openTask } from './taskWidgetState.js';

  let tasks = $state([]);
  let loading = $state(false);
  let errored = $state(false);
  let lastLoadKey = null;
  let version = 0;

  const currentUserId = $derived($authStore?.currentUser?.id ?? null);
  const personalWorkspaceId = $derived($workspacesStore.personalWorkspace?.id ?? null);

  $effect(() => {
    if (currentUserId && !personalWorkspaceId) {
      void workspacesStore.loadPersonalWorkspace();
    }

    const loadKey = currentUserId && personalWorkspaceId
      ? `${currentUserId}:${personalWorkspaceId}`
      : null;
    if (loadKey && loadKey !== lastLoadKey) {
      lastLoadKey = loadKey;
      load(personalWorkspaceId);
    } else if (!currentUserId && lastLoadKey !== null) {
      lastLoadKey = null;
      tasks = [];
    }
  });

  async function load(workspaceId) {
    const v = ++version;
    loading = true;
    errored = false;
    try {
      const response = await api.items.getAll({
        ql: `workspace_id = ${workspaceId}`,
        limit: 30,
        order_by: 'updated_at',
        // Hide tasks completed more than the default window ago, matching the
        // per-workspace TodoList done-range default.
        completed_since: completedSinceCutoff(),
      });
      if (v !== version) return;
      tasks = normalizeTaskResponse(response);
    } catch (err) {
      if (v !== version) return;
      console.error('Failed to load personal tasks:', err);
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
    <ListChecks class="w-6 h-6 mb-2 opacity-60" />
    <p class="text-sm">Couldn't load your personal tasks</p>
  </div>
{:else if tasks.length === 0}
  <div class="flex flex-col items-center text-center py-6" style="color: var(--ds-text-subtle);">
    <ListChecks class="w-6 h-6 mb-2 opacity-60" />
    <p class="text-sm">Your personal todo list is empty</p>
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
