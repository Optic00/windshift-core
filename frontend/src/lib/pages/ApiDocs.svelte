<script>
  import { onMount } from 'svelte';
  import ApiDocsSidebar from '../features/api-docs/ApiDocsSidebar.svelte';
  import { loadSpec, groupOperationsByTag } from '../features/api-docs/openapi-store.svelte.js';

  // ApiOperation pulls in marked + dompurify for description rendering;
  // dynamic-import keeps that weight out of the main bundle so this page
  // only pays for it when actually visited.
  let ApiOperation = $state(null);

  let spec = $state(null);
  let groups = $state([]);
  let loading = $state(true);
  let loadError = $state(null);
  let selectedId = $state(null);

  const allOperations = $derived(groups.flatMap((g) => g.operations));
  const selectedEntry = $derived(
    allOperations.find((e) => e.id === selectedId) || allOperations[0] || null
  );

  onMount(async () => {
    try {
      const [doc, operationModule] = await Promise.all([
        loadSpec(),
        import('../features/api-docs/ApiOperation.svelte'),
      ]);
      spec = doc;
      groups = groupOperationsByTag(doc);
      ApiOperation = operationModule.default;
      const hash = (typeof window !== 'undefined' && window.location.hash || '').replace(/^#/, '');
      if (hash) selectedId = hash;
    } catch (err) {
      console.error('Failed to load OpenAPI spec', err);
      loadError = err?.message || 'Failed to load OpenAPI spec';
    } finally {
      loading = false;
    }
  });

  function handleSelect(entry) {
    selectedId = entry.id;
    if (typeof window !== 'undefined') {
      // Keep the URL hash in sync without scrolling the page (we own scroll).
      const url = `${window.location.pathname}${window.location.search}#${entry.id}`;
      window.history.replaceState(null, '', url);
    }
  }
</script>

<div class="api-docs">
  {#if loading}
    <div class="state">Loading API reference…</div>
  {:else if loadError}
    <div class="state state--error" data-testid="api-docs-error">{loadError}</div>
  {:else if !spec || groups.length === 0}
    <div class="state">No operations are documented in the OpenAPI spec.</div>
  {:else}
    <ApiDocsSidebar {groups} selectedId={selectedEntry?.id} onselect={handleSelect} />
    <main class="main" data-testid="api-docs-main">
      {#if selectedEntry && ApiOperation}
        {@const Operation = ApiOperation}
        {#key selectedEntry.id}
          <Operation {spec} entry={selectedEntry} />
        {/key}
      {/if}
    </main>
  {/if}
</div>

<style>
  .api-docs {
    display: flex;
    height: calc(100vh - var(--ds-app-header-height, 0px));
    min-height: 0;
    width: 100%;
    background: var(--ds-surface);
    color: var(--ds-text);
  }
  .main {
    flex: 1 1 auto;
    min-width: 0;
    overflow-y: auto;
    background: var(--ds-surface);
  }
  .state {
    flex: 1 1 auto;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 48px 24px;
    color: var(--ds-text-subtle);
    font-size: 14px;
  }
  .state--error {
    color: var(--ds-text-danger);
  }
</style>
