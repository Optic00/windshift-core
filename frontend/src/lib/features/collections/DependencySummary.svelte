<script>
  import { AlertTriangle, GitBranch } from '@lucide/svelte';
  import { t } from '../../stores/i18n.svelte.js';
  import Tooltip from '../../components/Tooltip.svelte';
  import { splitDependencies, dependencyKey } from './dependencySummary.js';

  // Lightweight dependency/blocker hover summary for board cards.
  //
  // Pulls counts + linked item titles from the item's existing links, scoped
  // to the "Depends On" link type (see dependencySummary.js for the
  // direction semantics). The summary only renders when the item has at
  // least one such link, so cards stay clean otherwise. The hover popover
  // reuses the shared Tooltip component. The first pass is intentionally
  // counts + a short title list; a full graph is out of scope (see page:16).
  let {
    item,
    links = [],
    maxTitles = 4,
  } = $props();

  let currentItemId = $derived(Number(item?.id));

  let dependency = $derived(splitDependencies(links, currentItemId));

  let hasDependencies = $derived(dependency.blockers.length > 0 || dependency.blocking.length > 0);

  function keyOf(linked) {
    return dependencyKey(linked);
  }
</script>

{#if hasDependencies}
  <Tooltip
    placement="top"
    delay={{ open: 200, close: 0 }}
    class="inline-flex"
    contentClass="px-3 py-2 text-xs max-w-xs"
  >
    {#snippet tip()}
      {#if dependency.blockers.length > 0}
        <div class="mb-1.5">
          <div class="font-semibold mb-1" style="color: #fbbf24;">
            {t('collections.blockedBy')} ({dependency.blockers.length})
          </div>
          <ul class="space-y-0.5">
            {#each dependency.blockers.slice(0, maxTitles) as linked (linked.id)}
              <li class="flex items-center gap-1.5">
                <span class="font-mono opacity-70 flex-shrink-0">{keyOf(linked)}</span>
                <span class="truncate">{linked.title}</span>
              </li>
            {/each}
            {#if dependency.blockers.length > maxTitles}
              <li class="opacity-70">+{dependency.blockers.length - maxTitles}</li>
            {/if}
          </ul>
        </div>
      {/if}
      {#if dependency.blocking.length > 0}
        <div>
          <div class="font-semibold mb-1" style="color: #93c5fd;">
            {t('collections.blocking')} ({dependency.blocking.length})
          </div>
          <ul class="space-y-0.5">
            {#each dependency.blocking.slice(0, maxTitles) as linked (linked.id)}
              <li class="flex items-center gap-1.5">
                <span class="font-mono opacity-70 flex-shrink-0">{keyOf(linked)}</span>
                <span class="truncate">{linked.title}</span>
              </li>
            {/each}
            {#if dependency.blocking.length > maxTitles}
              <li class="opacity-70">+{dependency.blocking.length - maxTitles}</li>
            {/if}
          </ul>
        </div>
      {/if}
    {/snippet}

    <span
      class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-medium"
      style="background: var(--ds-surface); color: var(--ds-text-subtle);"
      data-testid={`board-card-dependency-summary-${currentItemId}`}
    >
      {#if dependency.blockers.length > 0}
        <AlertTriangle class="w-3 h-3 flex-shrink-0" style="color: #f59e0b;" />
        <span>{t('collections.blockersCount', { count: dependency.blockers.length })}</span>
      {/if}
      {#if dependency.blocking.length > 0}
        <GitBranch class="w-3 h-3 flex-shrink-0" style="color: #3b82f6;" />
        <span>{t('collections.blockingCount', { count: dependency.blocking.length })}</span>
      {/if}
    </span>
  </Tooltip>
{/if}
