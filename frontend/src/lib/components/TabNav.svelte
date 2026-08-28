<script>
  import { currentRoute, navigate } from '../router.js';

  let {
    tabs = [],        // Array of { id, label }
    basePath = '',    // e.g., '/tests/reports' or '/admin/statuses'
    paramName = 'subtab',  // Query param name
    defaultTab = ''   // Fallback tab ID
  } = $props();

  const activeTab = $derived($currentRoute.query?.[paramName] || defaultTab || tabs[0]?.id);

  function switchTab(tabId) {
    navigate(`${basePath}?${paramName}=${tabId}`);
  }

  function isActive(tab) {
    return activeTab === tab.id || tab.matches?.includes(activeTab);
  }
</script>

<div class="border-b" style="border-color: var(--ds-border);">
  <nav class="-mb-px flex space-x-8" aria-label="Tabs">
    {#each tabs as tab}
      <button
        onclick={() => switchTab(tab.id)}
        class="whitespace-nowrap py-4 px-1 border-b-2 font-medium text-sm transition-colors"
        style="{isActive(tab)
          ? 'border-color: var(--ds-interactive); color: var(--ds-interactive);'
          : 'border-color: transparent; color: var(--ds-text-subtle);'}"
        aria-current={isActive(tab) ? 'page' : undefined}
      >
        {tab.label}
      </button>
    {/each}
  </nav>
</div>
