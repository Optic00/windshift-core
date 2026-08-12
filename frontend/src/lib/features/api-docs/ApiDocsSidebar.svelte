<script>
  import MethodBadge from './MethodBadge.svelte';
  import Input from '../../components/Input.svelte';
  import { filterGroups } from './openapi-store.svelte.js';

  let {
    groups,
    selectedId = null,
    onselect = () => {},
  } = $props();

  let query = $state('');
  const visibleGroups = $derived(filterGroups(groups, query));
  const visibleCount = $derived(visibleGroups.reduce((n, g) => n + g.operations.length, 0));

  function handleKey(e, entry) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onselect(entry);
    }
  }
</script>

<aside class="sidebar" data-testid="api-docs-sidebar">
  <header class="sidebar-head">
    <h1 class="sidebar-title">API reference</h1>
    <p class="sidebar-meta">{groups.reduce((n, g) => n + g.operations.length, 0)} operations</p>
  </header>

  <div class="filter">
    <Input
      type="search"
      bind:value={query}
      placeholder="Filter operations…"
      class="filter-input"
      dataTestid="api-docs-filter"
      ariaLabel="Filter operations"
      size="small"
    />
    {#if query}
      <span class="filter-count">{visibleCount}</span>
    {/if}
  </div>

  <nav class="groups">
    {#each visibleGroups as group (group.tag)}
      <section class="group">
        <h2 class="group-tag">{group.tag}</h2>
        <ul class="group-ops">
          {#each group.operations as entry (entry.id)}
            <li>
              <a
                href={`#${entry.id}`}
                class="op-row"
                class:op-row--active={selectedId === entry.id}
                data-testid="api-docs-op-link"
                data-op-id={entry.id}
                onclick={(e) => { e.preventDefault(); onselect(entry); }}
                onkeydown={(e) => handleKey(e, entry)}
              >
                <MethodBadge method={entry.method} />
                <span class="op-path">{entry.path}</span>
              </a>
            </li>
          {/each}
        </ul>
      </section>
    {/each}
    {#if visibleGroups.length === 0}
      <p class="empty">No operations match “{query}”.</p>
    {/if}
  </nav>
</aside>

<style>
  .sidebar {
    width: 280px;
    flex-shrink: 0;
    border-right: 1px solid var(--ds-border);
    background: var(--ds-surface);
    overflow-y: auto;
    height: 100%;
    display: flex;
    flex-direction: column;
  }
  .sidebar-head {
    padding: 18px 18px 8px;
    border-bottom: 1px solid var(--ds-border);
  }
  .sidebar-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--ds-text);
    margin: 0;
  }
  .sidebar-meta {
    font-size: 12px;
    color: var(--ds-text-subtle);
    margin: 2px 0 0;
  }
  .filter {
    padding: 10px 18px;
    border-bottom: 1px solid var(--ds-border);
    position: relative;
  }
  :global(.filter-input) {
    width: 100%;
    padding: 6px 10px;
    font-size: 12.5px;
    background: var(--ds-surface);
    border: 1px solid var(--ds-border);
    border-radius: 4px;
    color: var(--ds-text);
  }
  :global(.filter-input):focus {
    outline: none;
    border-color: var(--ds-border-focused);
  }
  .filter-count {
    position: absolute;
    right: 26px;
    top: 50%;
    transform: translateY(-50%);
    font-size: 11px;
    color: var(--ds-text-subtle);
    pointer-events: none;
  }
  .groups {
    padding: 4px 0 24px;
    flex: 1 1 auto;
    overflow-y: auto;
  }
  .group {
    margin-top: 14px;
  }
  .group-tag {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--ds-text-subtle);
    margin: 0;
    padding: 4px 18px;
    font-weight: 600;
  }
  .group-ops {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .op-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 18px;
    text-decoration: none;
    color: var(--ds-text);
    font-size: 12.5px;
    line-height: 1.3;
    cursor: pointer;
  }
  .op-row:hover {
    background: var(--ds-surface-hovered);
  }
  .op-row--active {
    background: var(--ds-surface-selected);
  }
  .op-path {
    font-family: var(--ds-font-mono, ui-monospace, monospace);
    color: var(--ds-text);
    word-break: break-all;
  }
  .empty {
    padding: 16px 18px;
    color: var(--ds-text-subtle);
    font-size: 12.5px;
  }
</style>
