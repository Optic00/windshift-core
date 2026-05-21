<script>
  import { onDestroy } from 'svelte';
  import { draggable, dropTargetForElements } from '@atlaskit/pragmatic-drag-and-drop/element/adapter';
  import { attachClosestEdge, extractClosestEdge } from '@atlaskit/pragmatic-drag-and-drop-hitbox/closest-edge';
  import { api } from '../../api.js';
  import { navigate, currentRoute } from '../../router.js';
  import { t } from '../../stores/i18n.svelte.js';
  import { confirm } from '../../composables/useConfirm.js';
  import { errorToast } from '../../stores/toasts.svelte.js';
  import {
    IconArrowLeft as ArrowLeft,
    IconPlus as Plus,
    IconDots as Dots,
    IconBook as Book
  } from '@tabler/icons-svelte-runes';
  import DropdownMenu from '../../layout/DropdownMenu.svelte';
  import EmptyState from '../../components/EmptyState.svelte';
  import PageMoveDialog from './PageMoveDialog.svelte';
  import PagePermissionsDialog from './PagePermissionsDialog.svelte';
  import { pagesTreeRefresh } from './pagesTreeRefresh.svelte.js';
  import { pagesFocusTitle } from './pagesFocusTitle.svelte.js';

  let { workspaceId } = $props();

  let pages = $state([]);
  let loading = $state(true);
  let creating = $state(false);
  let moveDialogOpen = $state(false);
  let moveDialogPage = $state(null);
  let permsDialogOpen = $state(false);
  let permsDialogPage = $state(null);
  let dndState = $state(new Map());
  let setupTimeout;
  let setupCleanups = [];

  // The currently active page id comes from the route param, not local state —
  // navigating back/forward (or PagesView selecting via a different path) must
  // keep the sidebar's highlight in sync.
  let activePageId = $derived(
    $currentRoute?.params?.pageId ? Number($currentRoute.params.pageId) : null
  );

  onDestroy(() => {
    cleanupDnd();
  });

  // Initial load + external refresh in one effect. Reading
  // `pagesTreeRefresh.tick` makes the effect re-run when it bumps; the
  // effect's first invocation handles the mount-time load. Critically we
  // do NOT read `loading` here — loadTree() mutates it, which would
  // self-retrigger the effect into an infinite loop.
  $effect(() => {
    pagesTreeRefresh.tick;
    loadTree();
  });

  // Re-wire DnD whenever the rendered tree changes. The timeout matches
  // BoardConfigurationPage's pattern: the DOM nodes need to mount before
  // pragmatic-drag-and-drop can attach.
  $effect(() => {
    pages;
    if (typeof document === 'undefined') return;
    if (setupTimeout) clearTimeout(setupTimeout);
    setupTimeout = setTimeout(() => setupDnd(), 50);
  });

  async function loadTree() {
    loading = true;
    try {
      const resp = await api.pages.getTree(workspaceId);
      pages = flattenDepthFirst(resp.tree || []);
    } catch (err) {
      errorToast(err?.message || t('pages.errorLoadTree'));
    } finally {
      loading = false;
    }
  }

  function flattenDepthFirst(nodes) {
    const out = [];
    for (const node of nodes) {
      out.push(node);
      if (node.children?.length) {
        out.push(...flattenDepthFirst(node.children));
      }
    }
    return out;
  }

  function backToWorkspace() {
    navigate(`/workspaces/${workspaceId}`);
  }

  function selectPage(id) {
    navigate(`/workspaces/${workspaceId}/pages/${id}`);
  }

  async function createPage(parentId) {
    if (creating) return;
    creating = true;
    try {
      const page = await api.pages.createPage(workspaceId, {
        title: t('pages.untitled'),
        content: '',
        parentId: parentId ?? null,
      });
      pagesFocusTitle.request(page.id);
      navigate(`/workspaces/${workspaceId}/pages/${page.id}`);
      await loadTree();
    } catch (err) {
      errorToast(err?.message || t('pages.errorCreate'));
    } finally {
      creating = false;
    }
  }

  async function archivePage(page) {
    const ok = await confirm({
      title: t('pages.archiveTitle', { title: page.title }),
      message: t('pages.archiveMessage'),
      confirmText: t('pages.archiveConfirm'),
      cancelText: t('common.cancel'),
      variant: 'danger',
    });
    if (!ok) return;
    try {
      await api.pages.archivePage(workspaceId, page.id);
      if (activePageId === page.id) {
        navigate(`/workspaces/${workspaceId}/pages`);
      }
      await loadTree();
    } catch (err) {
      errorToast(err?.message || t('pages.errorArchive'));
    }
  }

  function requestRename(page) {
    if (activePageId !== page.id) {
      navigate(`/workspaces/${workspaceId}/pages/${page.id}`);
    }
    pagesFocusTitle.request(page.id);
  }

  function kebabItems(page) {
    return [
      { id: 'add-child', type: 'regular', icon: Plus, title: t('pages.menuAddChild'), onClick: () => createPage(page.id) },
      { id: 'rename', type: 'regular', title: t('pages.menuRename'), onClick: () => requestRename(page) },
      { id: 'move', type: 'regular', title: t('pages.menuMove'), onClick: () => { moveDialogPage = page; moveDialogOpen = true; } },
      { id: 'permissions', type: 'regular', title: t('pages.menuPermissions'), onClick: () => { permsDialogPage = page; permsDialogOpen = true; } },
      { id: 'divider', type: 'divider' },
      { id: 'archive', type: 'regular', title: t('pages.menuArchive'), color: 'var(--ds-text-danger)', onClick: () => archivePage(page) },
    ];
  }

  // --- DnD ---

  function cleanupDnd() {
    if (setupTimeout) clearTimeout(setupTimeout);
    setupCleanups.forEach((fn) => fn());
    setupCleanups = [];
    dndState = new Map();
  }

  function setupDnd() {
    cleanupDnd();
    /** @type {NodeListOf<HTMLElement>} */ (document.querySelectorAll('[data-page-row]')).forEach((element) => {
      const pageId = Number(element.dataset.pageRow);
      const page = pages.find((p) => p.id === pageId);
      if (!page) return;

      dndState.set(pageId, { closestEdge: null, over: false });

      const dragCleanup = draggable({
        element,
        getInitialData: () => ({ type: 'page', pageId, parentId: page.parent_id ?? null }),
        onDragStart: () => {
          element.style.opacity = '0.5';
        },
        onDrop: () => {
          element.style.opacity = '';
          dndState = new Map();
        },
      });

      const dropCleanup = dropTargetForElements({
        element,
        canDrop: ({ source }) => {
          if (source.data.type !== 'page') return false;
          // Forbid dropping a page onto itself or any of its own descendants.
          // Quick descendant check via the materialized `path` prefix that
          // the backend returns on each PageNode.
          if (source.data.pageId === pageId) return false;
          const dragged = pages.find((p) => p.id === source.data.pageId);
          if (dragged) {
            const draggedPrefix = `${dragged.path}${dragged.id}/`;
            if (page.path.startsWith(draggedPrefix)) return false;
          }
          return true;
        },
        getData: ({ input, element: el }) =>
          attachClosestEdge({}, { input, element: el, allowedEdges: ['top', 'bottom'] }),
        onDragEnter: ({ self }) => {
          const closestEdge = extractClosestEdge(self.data);
          dndState.set(pageId, { closestEdge, over: true });
          dndState = new Map(dndState);
        },
        onDragLeave: () => {
          dndState.set(pageId, { closestEdge: null, over: false });
          dndState = new Map(dndState);
        },
        onDrop: ({ self, source }) => {
          const closestEdge = extractClosestEdge(self.data);
          dndState = new Map();
          handleDrop(source.data.pageId, pageId, closestEdge);
        },
      });

      setupCleanups.push(() => {
        dragCleanup();
        dropCleanup();
      });
    });
  }

  async function handleDrop(draggedId, targetId, closestEdge) {
    if (draggedId === targetId) return;
    const target = pages.find((p) => p.id === targetId);
    if (!target) return;
    let newParentId;
    let prevSiblingId = null;
    let nextSiblingId = null;

    if (closestEdge === 'top' || closestEdge === 'bottom') {
      // Sibling drop: parent of dropped page becomes parent of target.
      newParentId = target.parent_id ?? null;
      // Identify the target's siblings (children of newParentId, in
      // display order) so we can pick the prev/next that bracket the
      // drop. `pages` is depth-first so siblings are contiguous when
      // filtered by parent_id; index by position to find neighbors.
      const siblings = pages.filter((p) => (p.parent_id ?? null) === newParentId);
      const targetIdx = siblings.findIndex((p) => p.id === targetId);
      if (targetIdx === -1) return;
      if (closestEdge === 'top') {
        nextSiblingId = target.id;
        prevSiblingId = targetIdx > 0 ? siblings[targetIdx - 1].id : null;
      } else {
        prevSiblingId = target.id;
        nextSiblingId = targetIdx < siblings.length - 1 ? siblings[targetIdx + 1].id : null;
      }
      // A sibling pointer that names the dragged page itself is
      // meaningless (it's about to move). Drop it so the server picks an
      // open-ended neighbor and the move still places correctly.
      if (prevSiblingId === draggedId) prevSiblingId = null;
      if (nextSiblingId === draggedId) nextSiblingId = null;
    } else {
      // Drop on the row body (no closest-edge match): make the dragged
      // page a child of the target. Position is "end of list" — server
      // computes a frac_index after the last existing child.
      newParentId = targetId;
    }

    try {
      await api.pages.movePage(workspaceId, draggedId, newParentId, {
        prevSiblingId,
        nextSiblingId,
      });
      await loadTree();
    } catch (err) {
      errorToast(err?.message || t('pages.errorMove'));
    }
  }
</script>

<aside class="pages-sidebar" data-testid="pages-nav-sidebar">
  <header class="header">
    <button class="back" type="button" onclick={backToWorkspace} data-testid="pages-back-button">
      <ArrowLeft size={16} />
      <span>{t('pages.backWorkspace')}</span>
    </button>
    <div class="title-row">
      <h2>{t('pages.treeHeading')}</h2>
      <button
        id="pages-add-button"
        class="add-button"
        type="button"
        onclick={() => createPage(activePageId)}
        disabled={creating}
        aria-label={t('pages.addPageAria')}
      >
        <Plus size={16} />
      </button>
    </div>
  </header>

  {#if loading}
    <p class="status">{t('pages.treeLoading')}</p>
  {:else if pages.length === 0}
    <div class="tree-empty">
      <EmptyState
        icon={Book}
        title={t('pages.treeEmptyTitle')}
        description={t('pages.treeEmptyDescription')}
      />
    </div>
  {:else}
    <ul class="tree" data-testid="page-tree">
      {#each pages as page (page.id)}
        {@const edge = dndState.get(page.id)?.closestEdge}
        {@const isOver = dndState.get(page.id)?.over}
        <li
          class="tree-item"
          class:active={activePageId === page.id}
          class:drop-top={edge === 'top'}
          class:drop-bottom={edge === 'bottom'}
          class:drop-on={isOver && !edge}
          data-page-row={page.id}
          data-testid="page-tree-item"
          data-page-id={page.id}
          style="padding-left: {0.5 + page.depth * 0.8}rem"
        >
          <button class="page-button" type="button" onclick={() => selectPage(page.id)}>
            {page.title}
          </button>
          <span class="kebab-slot">
            <DropdownMenu
              triggerIcon={Dots}
              triggerIconClass="w-4 h-4"
              items={kebabItems(page)}
              showChevron={false}
              iconOnly={true}
              placement="bottom-end"
              triggerClass="kebab-trigger"
              triggerTestid="page-kebab"
            />
          </span>
        </li>
      {/each}
    </ul>
  {/if}
</aside>

{#if moveDialogPage}
  <PageMoveDialog
    bind:isOpen={moveDialogOpen}
    {workspaceId}
    page={moveDialogPage}
    onMoved={loadTree}
  />
{/if}

{#if permsDialogPage}
  <PagePermissionsDialog
    bind:isOpen={permsDialogOpen}
    {workspaceId}
    pageId={permsDialogPage.id}
    onUpdated={loadTree}
  />
{/if}

<style>
  .pages-sidebar {
    display: flex;
    flex-direction: column;
    height: 100%;
    background: var(--ds-surface);
    border-right: 1px solid var(--ds-border);
    overflow-y: auto;
  }

  .header {
    padding: 0.75rem 0.75rem 0.5rem;
    border-bottom: 1px solid var(--ds-border);
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .back {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    background: transparent;
    border: none;
    padding: 0.25rem 0.375rem;
    color: var(--ds-text-subtle);
    font-size: 0.75rem;
    cursor: pointer;
    border-radius: 0.25rem;
    align-self: flex-start;
  }

  .back:hover {
    background: var(--ds-background-neutral-hovered);
    color: var(--ds-text);
  }

  .title-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 0.25rem;
  }

  .title-row h2 {
    margin: 0;
    font-size: 0.75rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--ds-text-subtle);
  }

  .add-button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.5rem;
    height: 1.5rem;
    border-radius: 0.25rem;
    border: none;
    background: transparent;
    color: var(--ds-text-subtle);
    cursor: pointer;
  }

  .add-button:hover:not(:disabled) {
    background: var(--ds-background-neutral-hovered);
    color: var(--ds-text);
  }

  .add-button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .tree {
    list-style: none;
    padding: 0.5rem 0;
    margin: 0;
  }

  .tree-item {
    position: relative;
    display: flex;
    align-items: center;
    gap: 0.25rem;
    padding-right: 0.5rem;
    transition: background-color var(--duration-fast, 100ms) ease;
  }

  .tree-item.active .page-button {
    background: var(--ds-surface-selected);
    font-weight: 500;
  }

  /* Drop affordances: a thin line for sibling drops, full-row tint for child drops */
  .tree-item.drop-top::before,
  .tree-item.drop-bottom::after {
    content: '';
    position: absolute;
    left: 0.25rem;
    right: 0.25rem;
    height: 2px;
    background: var(--ds-border-focused, #3b82f6);
    pointer-events: none;
  }

  .tree-item.drop-top::before {
    top: -1px;
  }

  .tree-item.drop-bottom::after {
    bottom: -1px;
  }

  .tree-item.drop-on {
    background: color-mix(in srgb, var(--ds-border-focused, #3b82f6) 12%, transparent);
  }

  .page-button {
    flex: 1;
    min-width: 0;
    text-align: left;
    background: transparent;
    border: none;
    padding: 0.375rem 0.5rem;
    font-size: 0.875rem;
    color: var(--ds-text);
    cursor: pointer;
    border-radius: 0.25rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .page-button:hover {
    background: var(--ds-background-neutral-hovered);
  }

  .kebab-slot {
    opacity: 0;
    transition: opacity var(--duration-fast, 100ms) ease;
  }

  .tree-item:hover .kebab-slot,
  .tree-item:focus-within .kebab-slot {
    opacity: 1;
  }

  :global(.kebab-trigger) {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.5rem;
    height: 1.5rem;
    border-radius: 0.25rem;
    border: none;
    background: transparent;
    color: var(--ds-text-subtle);
    cursor: pointer;
  }

  :global(.kebab-trigger:hover) {
    background: var(--ds-background-neutral-hovered);
    color: var(--ds-text);
  }

  .status {
    color: var(--ds-text-subtle);
    font-size: 0.875rem;
    padding: 1rem;
  }

  .tree-empty {
    padding: 0.5rem;
  }
</style>
