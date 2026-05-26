<script>
  import { createPopover, melt } from '@melt-ui/svelte';
  import { Link2, FileText, Trash2, Plus, X } from '@lucide/svelte';
  import { itemTypeIconMap } from '../../utils/icons.js';
  import { api } from '../../api.js';
  import { t } from '../../stores/i18n.svelte.js';
  import { errorToast } from '../../stores/toasts.svelte.js';
  import { useDebounce } from 'runed';
  import { untrack } from 'svelte';

  /**
   * Top-right popover on a page detail showing the work items linked to
   * this page. Read + unlink + add via inline work-item search. The
   * parent owns the pageLinks array and re-fetches on success — the
   * button itself just emits callbacks.
   */

  let {
    workspaceId,
    pageId,
    pageLinks = [],
    loading = false,
    pageLinkTypeId = null,
    onlinkCreated = () => {},
    onlinkRemoved = () => {},
  } = $props();

  const iconMap = itemTypeIconMap;

  const {
    elements: { trigger, content },
    states: { open },
  } = createPopover({
    forceVisible: true,
    positioning: { placement: 'bottom-end' },
    portal: 'body',
  });

  let count = $derived(pageLinks.length);
  let mode = $state('list'); // 'list' or 'add'
  let searchQuery = $state('');
  let searchResults = $state([]);
  let searching = $state(false);
  let highlightedIndex = $state(-1);
  let submitting = $state(false);

  $effect(() => {
    if (!$open) {
      mode = 'list';
      searchQuery = '';
      searchResults = [];
      highlightedIndex = -1;
    }
  });

  const runSearch = useDebounce(async (q) => {
    try {
      searching = true;
      const results = await api.links.search(q, 'item', 10);
      searchResults = Array.isArray(results) ? results : [];
      highlightedIndex = searchResults.length > 0 ? 0 : -1;
    } catch (err) {
      console.error('work-item search failed', err);
      searchResults = [];
    } finally {
      searching = false;
    }
  }, 250);

  // Reactive search. Mirror the LinkItemModal pattern: untrack() the debounce
  // call so we don't subscribe to its internal $state and loop.
  $effect(() => {
    const q = (searchQuery || '').trim();
    if (mode !== 'add') {
      untrack(() => runSearch.cancel?.());
      searchResults = [];
      highlightedIndex = -1;
      return;
    }
    if (q.length >= 2) {
      untrack(() => runSearch(q));
    } else {
      untrack(() => runSearch.cancel?.());
      searchResults = [];
      highlightedIndex = -1;
      searching = false;
    }
  });

  async function linkItem(item) {
    if (!pageLinkTypeId || submitting) return;
    submitting = true;
    try {
      const link = await api.links.create({
        link_type_id: pageLinkTypeId,
        source_type: 'item',
        source_id: item.id,
        target_type: 'page',
        target_id: pageId,
      });
      onlinkCreated(link);
      mode = 'list';
      searchQuery = '';
      searchResults = [];
    } catch (err) {
      errorToast(err?.message || t('pages.workItemsErrorLink'));
    } finally {
      submitting = false;
    }
  }

  async function unlinkItem(linkId) {
    try {
      await api.links.delete(linkId);
      onlinkRemoved(linkId);
    } catch (err) {
      errorToast(err?.message || t('pages.workItemsErrorUnlink'));
    }
  }

  function handleSearchKeyDown(e) {
    if (e.key === 'Escape') {
      e.preventDefault();
      mode = 'list';
      searchQuery = '';
      searchResults = [];
      return;
    }
    if (searchResults.length === 0) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      highlightedIndex = (highlightedIndex + 1) % searchResults.length;
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      highlightedIndex = highlightedIndex <= 0 ? searchResults.length - 1 : highlightedIndex - 1;
    } else if (e.key === 'Enter' && highlightedIndex >= 0) {
      e.preventDefault();
      e.stopPropagation();
      linkItem(searchResults[highlightedIndex]);
    }
  }
</script>

<button
  use:melt={$trigger}
  type="button"
  class="trigger"
  data-testid="page-work-items-trigger"
  aria-label={t('pages.workItemsAria')}
>
  <Link2 size={14} />
  <span class="trigger-label">{t('pages.workItemsButton')}</span>
  {#if count > 0}
    <span class="badge" data-testid="page-work-items-count">{count}</span>
  {/if}
</button>

{#if $open}
  <div use:melt={$content} class="popover" data-testid="page-work-items-popover">
    <header class="popover-header">
      <span class="title">{t('pages.workItemsTitle')}</span>
      {#if pageLinkTypeId != null && mode === 'list'}
        <button
          type="button"
          class="add-btn"
          onclick={() => (mode = 'add')}
          data-testid="page-work-items-add"
        >
          <Plus size={14} />
          {t('pages.addWorkItem')}
        </button>
      {/if}
      {#if mode === 'add'}
        <button
          type="button"
          class="add-btn"
          onclick={() => (mode = 'list')}
          data-testid="page-work-items-add-cancel"
        >
          <X size={14} />
          {t('pages.addWorkItemCancel')}
        </button>
      {/if}
    </header>

    {#if mode === 'add'}
      <div class="search-row">
        <input
          type="text"
          bind:value={searchQuery}
          onkeydown={handleSearchKeyDown}
          placeholder={t('pages.addWorkItemSearchPlaceholder')}
          class="search-input"
          data-testid="page-work-items-add-search"
        />
      </div>
      {#if searching}
        <p class="status">{t('common.loading')}</p>
      {:else if searchResults.length === 0}
        <p class="status">
          {#if searchQuery.trim().length < 2}
            {t('pickers.startTypingToSearch')}
          {:else}
            {t('pickers.noResultsFor', { query: searchQuery })}
          {/if}
        </p>
      {:else}
        <ul class="list">
          {#each searchResults as result, index}
            {@const isHighlighted = highlightedIndex === index}
            <li>
              <button
                type="button"
                class="row"
                class:row--highlighted={isHighlighted}
                onmouseenter={() => (highlightedIndex = index)}
                onclick={() => linkItem(result)}
                data-testid="page-work-items-add-result"
                data-item-id={result.id}
              >
                <div
                  class="row-icon"
                  style="background-color: {(result.item_type_color || '#6b7280')}20; color: {(result.item_type_color || '#6b7280')};"
                >
                  {#if result.item_type_icon && iconMap[result.item_type_icon]}
                    {@const I = iconMap[result.item_type_icon]}
                    <I size={12} />
                  {:else}
                    <FileText size={12} />
                  {/if}
                </div>
                <span class="row-title">{result.title}</span>
                <span class="row-meta">{result.workspace_name || ''}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    {:else}
      {#if loading}
        <p class="status">{t('pages.workItemsLoading')}</p>
      {:else if pageLinks.length > 0}
        <ul class="list">
          {#each pageLinks as link}
            {@const isCurrentPage = link.target_type === 'page' && link.target_id === pageId}
            {@const linkedItemId = isCurrentPage ? link.source_id : link.target_id}
            {@const linkedItemTitle = isCurrentPage ? link.source_title : link.target_title}
            {@const linkedItemWorkspaceKey = isCurrentPage ? link.source_workspace_key : link.target_workspace_key}
            {@const linkedItemWorkspaceId = isCurrentPage ? link.source_workspace_id : link.target_workspace_id}
            {@const linkedItemNumber = isCurrentPage ? link.source_item_number : link.target_item_number}
            {@const linkedItemStatus = isCurrentPage ? link.source_status_name : link.target_status_name}
            {@const linkedItemIconKey = isCurrentPage ? link.source_item_type_icon : link.target_item_type_icon}
            {@const linkedItemIconColor = isCurrentPage ? link.source_item_type_color : link.target_item_type_color}
            {@const linkedItemKey = `${linkedItemWorkspaceKey || 'WORK'}-${linkedItemNumber ?? linkedItemId}`}
            {@const linkedItemHref = `/workspaces/${linkedItemWorkspaceId || workspaceId}/items/${linkedItemId}`}
            <li
              class="row-li"
              data-testid="page-work-items-row"
              data-link-id={link.id}
              data-item-id={linkedItemId}
            >
              <a class="row row--link" href={linkedItemHref}>
                <div
                  class="row-icon"
                  style="background-color: {(linkedItemIconColor || '#6b7280')}20; color: {(linkedItemIconColor || '#6b7280')};"
                >
                  {#if linkedItemIconKey && iconMap[linkedItemIconKey]}
                    {@const I = iconMap[linkedItemIconKey]}
                    <I size={12} />
                  {:else}
                    <FileText size={12} />
                  {/if}
                </div>
                <span class="row-key">{linkedItemKey}</span>
                <span class="row-title">{linkedItemTitle}</span>
                {#if linkedItemStatus}
                  <span class="row-status">{linkedItemStatus}</span>
                {/if}
              </a>
              <button
                type="button"
                class="row-delete"
                aria-label={t('pages.removeWorkItemLink')}
                title={t('pages.removeWorkItemLink')}
                onclick={() => unlinkItem(link.id)}
                data-testid="page-work-items-unlink"
              >
                <Trash2 size={14} />
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  </div>
{/if}

<style>
  .trigger {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.25rem 0.5rem;
    border: 1px solid var(--ds-border);
    background: transparent;
    color: var(--ds-text-subtle);
    border-radius: 0.375rem;
    font-size: 0.75rem;
    cursor: pointer;
    transition: background 120ms, color 120ms;
  }
  .trigger:hover {
    background: var(--ds-surface-hover);
    color: var(--ds-text);
  }
  .trigger-label {
    font-weight: 500;
  }
  .badge {
    background: var(--ds-background-neutral);
    color: var(--ds-text);
    padding: 0 0.375rem;
    border-radius: 999px;
    font-size: 0.625rem;
    min-width: 1.25rem;
    text-align: center;
    line-height: 1rem;
  }

  .popover {
    z-index: 1000;
    width: 360px;
    max-height: 480px;
    background: var(--ds-surface);
    border: 1px solid var(--ds-border);
    border-radius: 0.5rem;
    box-shadow: 0 8px 24px rgb(0 0 0 / 0.12);
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .popover-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid var(--ds-border);
    font-size: 0.875rem;
  }
  .title {
    font-weight: 600;
    color: var(--ds-text);
  }
  .add-btn {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.25rem 0.5rem;
    background: transparent;
    border: 1px solid var(--ds-border);
    border-radius: 0.25rem;
    color: var(--ds-text-subtle);
    font-size: 0.75rem;
    cursor: pointer;
  }
  .add-btn:hover {
    color: var(--ds-text);
    background: var(--ds-surface-hover);
  }

  .search-row {
    padding: 0.5rem;
    border-bottom: 1px solid var(--ds-border);
  }
  .search-input {
    width: 100%;
    padding: 0.375rem 0.5rem;
    border: 1px solid var(--ds-border);
    border-radius: 0.25rem;
    background: var(--ds-surface);
    color: var(--ds-text);
    font-size: 0.875rem;
  }
  .search-input:focus {
    outline: none;
    border-color: var(--ds-accent-blue);
  }

  .status {
    padding: 1rem;
    margin: 0;
    color: var(--ds-text-subtle);
    font-size: 0.8125rem;
    text-align: center;
  }

  .list {
    list-style: none;
    padding: 0.25rem;
    margin: 0;
    max-height: 360px;
    overflow-y: auto;
  }
  .row-li {
    display: flex;
    align-items: center;
    gap: 0.25rem;
  }
  .row,
  .row--link {
    flex: 1 1 auto;
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem;
    background: transparent;
    border: none;
    border-radius: 0.25rem;
    color: var(--ds-text);
    text-decoration: none;
    text-align: left;
    font-size: 0.8125rem;
    cursor: pointer;
    min-width: 0;
  }
  .row:hover,
  .row--link:hover,
  .row--highlighted {
    background: var(--ds-surface-hover);
  }
  .row-icon {
    width: 22px;
    height: 22px;
    border-radius: 999px;
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .row-key {
    font-family: var(--ds-font-mono, ui-monospace, monospace);
    color: var(--ds-text-subtle);
    font-size: 0.6875rem;
    flex-shrink: 0;
  }
  .row-title {
    flex: 1 1 auto;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row-meta {
    color: var(--ds-text-subtle);
    font-size: 0.6875rem;
    flex-shrink: 0;
  }
  .row-status {
    background: var(--ds-background-neutral);
    color: var(--ds-text-subtle);
    padding: 0.125rem 0.375rem;
    border-radius: 999px;
    font-size: 0.6875rem;
    flex-shrink: 0;
  }
  .row-delete {
    padding: 0.375rem;
    background: transparent;
    border: none;
    color: var(--ds-text-subtle);
    border-radius: 0.25rem;
    cursor: pointer;
  }
  .row-delete:hover {
    color: var(--ds-text-danger);
    background: var(--ds-surface-hover);
  }
</style>
