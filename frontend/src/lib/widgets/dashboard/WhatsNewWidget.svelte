<script>
  import { Bell, Eye, Inbox } from '@lucide/svelte';
  import { homepageStore, workspacesStore } from '../../stores';
  import { navigate } from '../../router.js';

  const MAX_ENTRIES_PER_WORKSPACE = 5;
  const MAX_WORKSPACES = 5;

  let notifications = $derived(homepageStore.notifications);
  let watchedItems = $derived(homepageStore.watchedItems);
  let loading = $derived(homepageStore.loading);

  let workspaceMap = $derived(
    new Map(($workspacesStore.allWorkspaces || []).map((w) => [w.id, w]))
  );

  let groups = $derived.by(() => {
    const entries = [];

    for (const n of notifications) {
      const m = n.action_url?.match(/^\/workspaces\/(\d+)\//);
      if (!m) continue;
      entries.push({
        id: `n-${n.id}`,
        workspaceId: parseInt(m[1], 10),
        timestamp: n.timestamp,
        source: 'notification',
        title: n.message || n.title || 'Notification',
        subtitle: null,
        link: n.action_url,
        read: n.read,
      });
    }

    for (const w of watchedItems) {
      if (!w.last_activity) continue;
      entries.push({
        id: `w-${w.item_id}`,
        workspaceId: w.workspace_id,
        timestamp: w.last_activity,
        source: 'watched',
        title: w.title,
        subtitle: w.status,
        link: `/workspaces/${w.workspace_id}/items/${w.item_id}`,
        read: true,
      });
    }

    const buckets = new Map();
    for (const e of entries) {
      const ws = workspaceMap.get(e.workspaceId);
      if (!ws) continue;
      let bucket = buckets.get(e.workspaceId);
      if (!bucket) {
        bucket = { workspaceId: e.workspaceId, workspaceName: ws.name, entries: [] };
        buckets.set(e.workspaceId, bucket);
      }
      bucket.entries.push(e);
    }

    const ts = (v) => (v ? new Date(v).getTime() : 0);

    const result = Array.from(buckets.values()).map((b) => {
      b.entries.sort((a, c) => ts(c.timestamp) - ts(a.timestamp));
      b.entries = b.entries.slice(0, MAX_ENTRIES_PER_WORKSPACE);
      b.newest = b.entries[0]?.timestamp;
      return b;
    });

    result.sort((a, b) => ts(b.newest) - ts(a.newest));
    return result.slice(0, MAX_WORKSPACES);
  });

  function open(entry) {
    if (entry?.link) navigate(entry.link);
  }
</script>

{#if loading && groups.length === 0}
  <div class="space-y-2 animate-pulse">
    {#each Array(3) as _}
      <div class="h-10 rounded" style="background-color: var(--ds-background-neutral);"></div>
    {/each}
  </div>
{:else if groups.length === 0}
  <div class="flex flex-col items-center text-center py-6" style="color: var(--ds-text-subtle);">
    <Inbox class="w-6 h-6 mb-2 opacity-60" />
    <p class="text-sm">You're all caught up</p>
  </div>
{:else}
  <div class="flex flex-col gap-3">
    {#each groups as g (g.workspaceId)}
      <section class="flex flex-col">
        <h3
          class="text-[0.65rem] uppercase tracking-wide font-medium mb-1 px-1"
          style="color: var(--ds-text-subtle);"
        >
          {g.workspaceName}
        </h3>
        <ul class="flex flex-col divide-y" style="border-color: var(--ds-border);">
          {#each g.entries as e (e.id)}
            <li>
              <button
                class="w-full text-left px-1 py-2 flex items-start gap-2 transition-colors"
                style="color: var(--ds-text);"
                onmouseenter={(e2) =>
                  (e2.currentTarget.style.backgroundColor = 'var(--ds-background-neutral-hovered)')}
                onmouseleave={(e2) => (e2.currentTarget.style.backgroundColor = '')}
                onclick={() => open(e)}
              >
                {#if e.source === 'watched'}
                  <Eye
                    class="w-4 h-4 mt-0.5 flex-shrink-0"
                    style="color: var(--ds-text-subtlest);"
                  />
                {:else}
                  <Bell
                    class="w-4 h-4 mt-0.5 flex-shrink-0"
                    style={e.read
                      ? 'color: var(--ds-text-subtlest);'
                      : 'color: var(--ds-icon-accent);'}
                  />
                {/if}
                <div class="min-w-0 flex-1">
                  <p
                    class="text-xs truncate"
                    style={e.read ? 'color: var(--ds-text-subtle);' : 'font-weight: 500;'}
                  >
                    {e.title}
                  </p>
                  <p
                    class="text-[0.65rem] mt-0.5 flex items-center gap-1.5"
                    style="color: var(--ds-text-subtlest);"
                  >
                    {#if e.subtitle}<span class="truncate">{e.subtitle}</span><span>·</span>{/if}
                    <span>{homepageStore.formatRelativeTime(e.timestamp)}</span>
                  </p>
                </div>
              </button>
            </li>
          {/each}
        </ul>
      </section>
    {/each}
  </div>
{/if}
