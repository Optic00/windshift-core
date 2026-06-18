<script>
  import { onMount } from 'svelte';
  import { Star, Play, Loader } from '@lucide/svelte';
  import { api } from '../api.js';
  import { navigate } from '../router.js';
  import { timerStore } from '../stores/timerStore.svelte.js';
  import { renderMarkdown } from '../utils/render-markdown.js';
  import { formatDate } from '../utils/dateFormatter.js';
  import MobileHeader from './MobileHeader.svelte';
  import Comments from '../features/items/Comments.svelte';

  let { itemId } = $props();

  let item = $state(null);
  let loading = $state(true);
  let errored = $state(false);
  let transitions = $state([]);
  let transitioning = $state(false);
  let isWatching = $state(false);
  let watchBusy = $state(false);
  let personalTaskCount = $state(0);
  let startingTimer = $state(false);

  const descriptionHtml = $derived(item?.description ? renderMarkdown(item.description) : '');
  const itemKey = $derived(
    item?.workspace_key && item?.workspace_item_number
      ? `${item.workspace_key}-${item.workspace_item_number}`
      : ''
  );
  const projectId = $derived(item?.time_project_id ?? item?.effective_project_id ?? null);
  const canStartTimer = $derived(!!item && !timerStore.hasActive && !!projectId);

  async function loadItem() {
    loading = true;
    errored = false;
    try {
      item = await api.items.get(itemId);
    } catch (err) {
      console.error('Failed to load item:', err);
      errored = true;
    } finally {
      loading = false;
    }
  }

  async function loadAux() {
    // Best-effort side data — failures here shouldn't blank the screen.
    try {
      const res = await api.items.getAvailableStatusTransitions(itemId);
      transitions = res?.available_transitions ?? [];
    } catch (err) {
      console.error('Failed to load transitions:', err);
    }
    try {
      const res = await api.items.getWatchStatus(itemId);
      isWatching = res?.watching || false;
    } catch (err) {
      console.error('Failed to load watch status:', err);
    }
    try {
      const tasks = await api.items.getPersonalTasks(itemId);
      personalTaskCount = Array.isArray(tasks) ? tasks.length : (tasks?.items?.length ?? 0);
    } catch {
      personalTaskCount = 0;
    }
  }

  async function changeStatus(statusId) {
    if (transitioning) return;
    transitioning = true;
    try {
      const updated = await api.items.transition(itemId, statusId);
      item = { ...item, ...updated };
      await loadAux();
    } catch (err) {
      console.error('Failed to transition item:', err);
    } finally {
      transitioning = false;
    }
  }

  async function toggleWatch() {
    if (watchBusy) return;
    watchBusy = true;
    try {
      if (isWatching) {
        await api.items.removeWatch(itemId);
        isWatching = false;
      } else {
        await api.items.addWatch(itemId);
        isWatching = true;
      }
    } catch (err) {
      console.error('Failed to toggle watch:', err);
    } finally {
      watchBusy = false;
    }
  }

  async function startTimer() {
    if (!canStartTimer || startingTimer) return;
    startingTimer = true;
    try {
      await timerStore.start({
        workspace_id: item.workspace_id,
        item_id: item.id,
        project_id: projectId,
        description: `Working on ${item.title}`,
      });
      navigate('/m/timer');
    } catch (err) {
      console.error('Failed to start timer:', err);
    } finally {
      startingTimer = false;
    }
  }

  onMount(async () => {
    await loadItem();
    if (!errored) loadAux();
  });
</script>

<MobileHeader title={itemKey} onback={() => (window.history.length > 1 ? window.history.back() : navigate('/m'))} />

{#if loading}
  <div class="center" data-testid="detail-loading"><Loader class="spin" size={22} /></div>
{:else if errored || !item}
  <p class="msg" data-testid="detail-error">Couldn't load this item.</p>
{:else}
  <div class="detail" data-testid="mobile-item-detail">
    <div class="status-line">
      {#if item.item_type_name}<span class="type">{item.item_type_name}</span>{/if}
      {#if item.status_name}
        <span
          class="status"
          style={item.status_color ? `background-color: ${item.status_color}1f; color: ${item.status_color};` : ''}
          data-testid="detail-status"
        >{item.status_name}</span>
      {/if}
    </div>

    <h1 class="title" data-testid="detail-title">{item.title}</h1>

    <!-- Status transitions -->
    {#if transitions.length > 0}
      <div class="transitions" data-testid="detail-transitions">
        {#each transitions as tr (tr.id)}
          <button
            class="chip"
            disabled={transitioning}
            onclick={() => changeStatus(tr.id)}
            data-testid="detail-transition"
            style={tr.category_color ? `border-color: ${tr.category_color};` : ''}
            type="button"
          >{tr.name}</button>
        {/each}
      </div>
    {/if}

    {#if descriptionHtml}
      <div class="html-content desc" data-testid="detail-description">{@html descriptionHtml}</div>
    {/if}

    <!-- Meta -->
    <dl class="meta">
      {#if item.assignee_name}
        <div><dt>Assignee</dt><dd>{item.assignee_name}</dd></div>
      {/if}
      {#if item.due_date}
        <div><dt>Due</dt><dd>{formatDate(item.due_date)}</dd></div>
      {/if}
      {#if personalTaskCount > 0}
        <div><dt>Personal tasks</dt><dd>{personalTaskCount} linked</dd></div>
      {/if}
    </dl>

    <!-- Actions -->
    <div class="actions">
      <button class="act" class:on={isWatching} onclick={toggleWatch} disabled={watchBusy} data-testid="detail-watch" type="button">
        <Star size={16} fill={isWatching ? 'currentColor' : 'none'} />
        {isWatching ? 'Watching' : 'Watch'}
      </button>
      {#if canStartTimer}
        <button class="act" onclick={startTimer} disabled={startingTimer} data-testid="detail-start-timer" type="button">
          <Play size={16} /> Start timer
        </button>
      {/if}
    </div>

    <!-- Comments -->
    <section class="comments">
      <Comments {itemId} />
    </section>
  </div>
{/if}

<style>
  .center { display: flex; justify-content: center; padding: 3rem; color: var(--ds-text-subtle); }
  :global(.spin) { animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }

  .msg { padding: 3rem 1.25rem; text-align: center; color: var(--ds-text-subtle); }

  .detail { padding: 0.75rem 0.875rem 2rem; }

  .status-line { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; }
  .type { font-size: 0.75rem; color: var(--ds-text-subtle); text-transform: uppercase; letter-spacing: 0.02em; }
  .status {
    display: inline-flex; align-items: center; border-radius: var(--radius-sm, 4px);
    padding: 1px 8px; font-size: 0.75rem; font-weight: var(--font-medium, 500);
    background-color: var(--ds-background-neutral); color: var(--ds-text-subtle);
  }

  .title { font-size: 1.25rem; font-weight: var(--font-semibold, 600); color: var(--ds-text); margin: 0 0 0.75rem; line-height: 1.3; }

  .transitions { display: flex; flex-wrap: wrap; gap: 0.5rem; margin-bottom: 1rem; }
  .chip {
    padding: 0.45rem 0.85rem; border: 1px solid var(--ds-border); border-radius: var(--radius-full, 9999px);
    background-color: var(--ds-surface); color: var(--ds-text); font-size: 0.8125rem; cursor: pointer; min-height: 36px;
  }
  .chip:disabled { opacity: 0.5; }

  .desc {
    padding: 0.5rem 0 1rem; border-bottom: 1px solid var(--ds-border); margin-bottom: 1rem;
    color: var(--ds-text); font-size: 0.9375rem; line-height: 1.55; overflow-wrap: anywhere;
  }

  .meta { margin: 0 0 1rem; display: flex; flex-direction: column; gap: 0.6rem; }
  .meta div { display: flex; justify-content: space-between; gap: 1rem; }
  .meta dt { color: var(--ds-text-subtle); font-size: 0.8125rem; margin: 0; }
  .meta dd { color: var(--ds-text); font-size: 0.875rem; margin: 0; text-align: right; }

  .actions { display: flex; gap: 0.5rem; margin-bottom: 1.5rem; }
  .act {
    flex: 1; display: inline-flex; align-items: center; justify-content: center; gap: 0.4rem;
    min-height: 44px; border: 1px solid var(--ds-border); border-radius: var(--radius-lg, 8px);
    background-color: var(--ds-surface); color: var(--ds-text); font-size: 0.875rem; font-weight: var(--font-medium, 500); cursor: pointer;
  }
  .act.on { color: var(--ds-interactive); border-color: var(--ds-interactive); }
  .act:disabled { opacity: 0.6; }

  .comments { border-top: 1px solid var(--ds-border); padding-top: 1rem; }
</style>
