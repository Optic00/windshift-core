<script>
  import { authStore } from '../stores';
  import { api } from '../api.js';
  import { formatRelativeCompact } from '../utils/dateFormatter.js';
  import MobileHeader from './MobileHeader.svelte';
  import MobileItemRow from './MobileItemRow.svelte';
  import UserAvatar from '../components/UserAvatar.svelte';

  const SEGMENTS = [
    { id: 'assigned', label: 'Assigned' },
    { id: 'watched', label: 'Watched' },
    { id: 'recent', label: 'Recent' },
  ];

  let segment = $state('assigned');
  // Per-segment cache so switching tabs doesn't refetch every time.
  let data = $state({ assigned: null, watched: null, recent: null });
  let loading = $state(false);
  let errored = $state(false);
  let version = 0;

  const currentUserId = $derived($authStore?.currentUser?.id ?? null);
  const rows = $derived(data[segment] ?? []);

  function key(i) {
    return i.workspace_key && i.workspace_item_number
      ? `${i.workspace_key}-${i.workspace_item_number}`
      : null;
  }

  function loadAssigned(raw) {
    const list = Array.isArray(raw) ? raw : (raw?.items ?? []);
    return list
      .filter((i) => i?.id)
      .map((i) => ({
        itemId: i.id,
        itemKey: key(i),
        title: i.title,
        statusName: i.status_name,
        statusColor: i.status_color,
        priorityName: i.priority_name,
        priorityColor: i.priority_color,
        dueDate: i.due_date ? new Date(i.due_date) : null,
      }));
  }

  function loadWatched(homepage) {
    return (homepage?.watched_items ?? [])
      .filter((w) => w?.item_id)
      .map((w) => ({
        itemId: w.item_id,
        itemKey: key(w),
        title: w.title,
        statusName: w.status,
        statusColor: w.status_color,
        priorityName: w.priority_name,
        priorityColor: w.priority_color,
        timestamp: w.last_activity ? formatRelativeCompact(new Date(w.last_activity)) : null,
      }));
  }

  function loadRecent(homepage) {
    const sources = [
      ...(homepage?.recently_viewed ?? []),
      ...(homepage?.recently_edited ?? []),
      ...(homepage?.recently_commented ?? []),
    ];
    const seen = new Set();
    const merged = [];
    for (const a of sources) {
      if (!a?.item_id || seen.has(a.item_id)) continue;
      seen.add(a.item_id);
      merged.push({
        itemId: a.item_id,
        itemKey: key(a),
        title: a.title,
        statusName: a.status,
        statusColor: a.status_color,
        timestamp: a.last_activity ? formatRelativeCompact(new Date(a.last_activity)) : null,
        _ts: a.last_activity ? new Date(a.last_activity).getTime() : 0,
      });
    }
    merged.sort((a, b) => b._ts - a._ts);
    return merged;
  }

  async function load(seg) {
    const v = ++version;
    loading = true;
    errored = false;
    try {
      if (seg === 'assigned') {
        if (!currentUserId) {
          data = { ...data, assigned: [] };
          return;
        }
        const res = await api.items.getAll({
          ql: `assignee_id = ${currentUserId} AND status_completed = false`,
          limit: 30,
          order_by: 'updated_at',
        });
        if (v !== version) return;
        data = { ...data, assigned: loadAssigned(res) };
      } else {
        // Watched + Recent both come from the homepage payload — fetch once.
        const homepage = await api.homepage.get();
        if (v !== version) return;
        data = { ...data, watched: loadWatched(homepage), recent: loadRecent(homepage) };
      }
    } catch (err) {
      if (v !== version) return;
      console.error('Failed to load my-work segment:', seg, err);
      errored = true;
    } finally {
      if (v === version) loading = false;
    }
  }

  function selectSegment(id) {
    segment = id;
    if (data[id] === null) load(id);
  }

  // Initial load of the default segment once the user id is known.
  let bootstrapped = false;
  $effect(() => {
    if (!bootstrapped && currentUserId) {
      bootstrapped = true;
      load('assigned');
    }
  });
</script>

<MobileHeader title="My Work">
  {#snippet right()}
    <UserAvatar />
  {/snippet}
  {#snippet children()}
    <div class="segmented" role="tablist" data-testid="my-work-segments">
      {#each SEGMENTS as s (s.id)}
        <button
          role="tab"
          aria-selected={segment === s.id}
          class="seg"
          class:active={segment === s.id}
          data-testid={`my-work-segment-${s.id}`}
          onclick={() => selectSegment(s.id)}
          type="button"
        >{s.label}</button>
      {/each}
    </div>
  {/snippet}
</MobileHeader>

<div class="list" data-testid="my-work-list">
  {#if loading && rows.length === 0}
    <div class="skeleton">
      {#each Array(5) as _}
        <div class="sk-row"></div>
      {/each}
    </div>
  {:else if errored}
    <p class="msg" data-testid="my-work-error">Couldn't load your work. Pull down or switch tabs to retry.</p>
  {:else if rows.length === 0}
    <p class="msg" data-testid="my-work-empty">
      {segment === 'assigned' ? 'Nothing assigned to you right now.' : segment === 'watched' ? "You aren't watching any items." : 'No recent activity.'}
    </p>
  {:else}
    {#each rows as row (row.itemId)}
      <MobileItemRow {...row} />
    {/each}
  {/if}
</div>

<style>
  .segmented {
    display: flex;
    gap: 0.25rem;
    padding: 0.5rem 0.75rem;
  }

  .seg {
    flex: 1;
    min-height: 36px;
    border: 1px solid var(--ds-border);
    border-radius: var(--radius-md, 6px);
    background-color: var(--ds-surface);
    color: var(--ds-text-subtle);
    font-size: 0.8125rem;
    font-weight: var(--font-medium, 500);
    cursor: pointer;
  }

  .seg.active {
    background-color: var(--ds-surface-selected, var(--ds-background-neutral));
    color: var(--ds-text);
    border-color: var(--ds-border-bold, var(--ds-border));
  }

  .msg {
    padding: 2rem 1.25rem;
    text-align: center;
    color: var(--ds-text-subtle);
    font-size: 0.875rem;
  }

  .skeleton {
    display: flex;
    flex-direction: column;
  }

  .sk-row {
    height: 56px;
    border-bottom: 1px solid var(--ds-border);
    background: linear-gradient(90deg, var(--ds-surface) 0%, var(--ds-background-neutral) 50%, var(--ds-surface) 100%);
    background-size: 200% 100%;
    animation: shimmer 1.2s ease-in-out infinite;
  }

  @keyframes shimmer {
    0% { background-position: 200% 0; }
    100% { background-position: -200% 0; }
  }
</style>
