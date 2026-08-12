<script>
  import { ChevronLeft, Search, X } from '@lucide/svelte';
  import { api } from '../api.js';
  import { navigate } from '../router.js';
  import { formatItemKey } from '../utils/itemKey.js';
  import MobileItemRow from './MobileItemRow.svelte';
  import Input from '../components/Input.svelte';

  let query = $state('');
  let results = $state([]);
  let loading = $state(false);
  let searched = $state(false);
  let inputEl = $state(null);
  let version = 0;
  let debounceTimer = null;

  function back() {
    if (window.history.length > 1) window.history.back();
    else navigate('/m');
  }

  function normalize(res) {
    const list = Array.isArray(res) ? res : (res?.data ?? res?.items ?? []);
    return list
      .filter((i) => i?.id)
      .map((i) => ({
        itemId: i.id,
        itemKey: formatItemKey(i),
        title: i.title,
        statusName: i.status_name ?? i.status,
        statusColor: i.status_color,
        priorityName: i.priority_name,
        priorityColor: i.priority_color,
      }));
  }

  async function run(q) {
    const trimmed = q.trim();
    if (!trimmed) {
      results = [];
      searched = false;
      return;
    }
    const v = ++version;
    loading = true;
    try {
      const res = await api.search.items({ query: trimmed, limit: 30 });
      if (v !== version) return;
      results = normalize(res);
      searched = true;
    } catch (err) {
      if (v !== version) return;
      console.error('Search failed:', err);
      results = [];
      searched = true;
    } finally {
      if (v === version) loading = false;
    }
  }

  function onInput() {
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => run(query), 250);
  }

  function clear() {
    query = '';
    results = [];
    searched = false;
    inputEl?.focus();
  }

  $effect(() => {
    inputEl?.focus();
  });
</script>

<header class="search-bar" data-testid="mobile-search-bar">
  <button class="back" onclick={back} aria-label="Back" type="button">
    <ChevronLeft size={24} />
  </button>
  <div class="field">
    <Search size={16} class="s-icon" />
    <Input
      bind:inputRef={inputEl}
      bind:value={query}
      oninput={onInput}
      type="search"
      enterkeyhint="search"
      placeholder="Search work items…"
      dataTestid="mobile-search-input"
      autocomplete="off"
      class="mobile-search-input !p-0"
    />
    {#if query}
      <button class="clear" onclick={clear} aria-label="Clear" type="button"><X size={16} /></button>
    {/if}
  </div>
</header>

<div class="results" data-testid="mobile-search-results">
  {#if loading && results.length === 0}
    <p class="msg">Searching…</p>
  {:else if !query.trim()}
    <p class="msg" data-testid="search-prompt">Search by title, key, or text across items you can see.</p>
  {:else if searched && results.length === 0}
    <p class="msg" data-testid="search-empty">No items match “{query.trim()}”.</p>
  {:else}
    {#each results as row (row.itemId)}
      <MobileItemRow {...row} />
    {/each}
  {/if}
</div>

<style>
  .search-bar {
    position: sticky;
    top: 0;
    z-index: 30;
    display: flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.5rem 0.75rem;
    padding-top: calc(env(safe-area-inset-top, 0px) + 0.5rem);
    background-color: var(--ds-surface);
    border-bottom: 1px solid var(--ds-border);
  }

  .back {
    display: inline-flex; align-items: center; justify-content: center;
    width: 36px; height: 36px; margin-left: -6px;
    border: none; background: transparent; color: var(--ds-text); cursor: pointer; flex-shrink: 0;
  }

  .field {
    flex: 1 1 auto; display: flex; align-items: center; gap: 0.4rem; min-width: 0;
    background-color: var(--ds-background-input, var(--ds-surface-raised));
    border: 1px solid var(--ds-border); border-radius: var(--radius-md, 6px);
    padding: 0 0.5rem; height: 38px;
  }
  .field :global(.s-icon) { color: var(--ds-text-subtle); flex-shrink: 0; }
  .field :global(.mobile-search-input) {
    flex: 1 1 auto; min-width: 0; height: 100%;
    border: none; outline: none; background: transparent;
    color: var(--ds-text); font-size: 1rem; /* >=16px avoids iOS zoom-on-focus */
  }
  .clear {
    border: none; background: transparent; color: var(--ds-text-subtle);
    display: inline-flex; align-items: center; cursor: pointer; padding: 2px; flex-shrink: 0;
  }

  .msg { padding: 2rem 1.25rem; text-align: center; color: var(--ds-text-subtle); font-size: 0.875rem; }
</style>
