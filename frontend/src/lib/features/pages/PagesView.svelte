<script>
  import { onMount } from 'svelte';
  import { api } from '../../api.js';
  import { navigate } from '../../router.js';
  import LazyMilkdownEditor from '../../editors/LazyMilkdownEditor.svelte';
  import PagePermissionsDialog from './PagePermissionsDialog.svelte';
  import PageMoveDialog from './PageMoveDialog.svelte';
  import { parseMarkdownHeadings, slugify } from './markdownToc.js';

  /**
   * Workspace knowledge-pages view: left tree + right Markdown editor.
   *
   * Phase 1 deliberately co-locates tree, editor, and save controls in a
   * single Svelte component. Future slices may split out PageTree,
   * PageHistoryDialog, and PagePermissionsDialog as the surface grows.
   */
  let { workspaceId, pageId = null } = $props();

  let pages = $state([]);
  // selectedId mirrors the route's pageId rather than being an
  // independently mutable $state — Svelte's compiler warns that
  // `$state(pageId)` only captures the initial prop, and the effect
  // below would never react to navigations back to the bare /pages
  // route without this.
  let selectedId = $state(null);
  let selectedPage = $state(null);
  let draftTitle = $state('');
  let draftContent = $state('');
  let dirty = $state(false);
  let loadingTree = $state(true);
  let loadingPage = $state(false);
  let saving = $state(false);
  let error = $state('');
  let creating = $state(false);
  let newTitle = $state('');
  let permsDialogOpen = $state(false);
  let moveDialogOpen = $state(false);

  // Headings are re-parsed from draftContent reactively. We use draftContent
  // rather than selectedPage.content so the TOC tracks unsaved edits.
  let headings = $derived(parseMarkdownHeadings(draftContent));

  onMount(async () => {
    await loadTree();
    if (pageId) {
      selectedId = pageId;
      await loadPage(pageId);
    }
  });

  // React to route changes in both directions: navigating to a different
  // page id loads it; navigating back to the bare /pages clears the
  // selection so a stale page doesn't keep rendering. Previously the
  // effect only branched on a truthy pageId.
  $effect(() => {
    if (pageId === selectedId) return;
    selectedId = pageId;
    if (pageId) {
      loadPage(pageId);
    } else {
      selectedPage = null;
      draftTitle = '';
      draftContent = '';
      dirty = false;
    }
  });

  async function loadTree() {
    loadingTree = true;
    error = '';
    try {
      const resp = await api.pages.getTree(workspaceId);
      pages = resp.pages || [];
    } catch (err) {
      error = err?.message || 'Failed to load pages';
    } finally {
      loadingTree = false;
    }
  }

  async function loadPage(id) {
    loadingPage = true;
    error = '';
    try {
      selectedPage = await api.pages.getPage(workspaceId, id);
      draftTitle = selectedPage.title;
      draftContent = selectedPage.content;
      dirty = false;
    } catch (err) {
      error = err?.message || 'Failed to load page';
      selectedPage = null;
    } finally {
      loadingPage = false;
    }
  }

  function selectPage(id) {
    if (dirty && !confirm('You have unsaved changes. Discard?')) return;
    navigate(`/workspaces/${workspaceId}/pages/${id}`);
  }

  async function savePage() {
    if (!selectedPage) return;
    saving = true;
    error = '';
    try {
      const updated = await api.pages.updatePage(workspaceId, selectedPage.id, {
        title: draftTitle,
        content: draftContent,
      });
      selectedPage = updated;
      draftTitle = updated.title;
      draftContent = updated.content;
      dirty = false;
      await loadTree();
    } catch (err) {
      error = err?.message || 'Failed to save';
    } finally {
      saving = false;
    }
  }

  async function createPage() {
    if (!newTitle.trim()) return;
    creating = true;
    error = '';
    try {
      const page = await api.pages.createPage(workspaceId, {
        title: newTitle.trim(),
        content: '',
        parentId: selectedPage?.id ?? null,
      });
      newTitle = '';
      await loadTree();
      navigate(`/workspaces/${workspaceId}/pages/${page.id}`);
    } catch (err) {
      error = err?.message || 'Failed to create page';
    } finally {
      creating = false;
    }
  }

  async function archivePage() {
    if (!selectedPage) return;
    if (!confirm(`Archive "${selectedPage.title}" and all child pages? This cannot be undone in Phase 1.`)) return;
    try {
      await api.pages.archivePage(workspaceId, selectedPage.id);
      selectedPage = null;
      await loadTree();
      navigate(`/workspaces/${workspaceId}/pages`);
    } catch (err) {
      error = err?.message || 'Failed to archive';
    }
  }

  function onContentInput(value) {
    draftContent = value;
    if (selectedPage && (value !== selectedPage.content || draftTitle !== selectedPage.title)) {
      dirty = true;
    }
  }

  function onTitleInput(event) {
    draftTitle = event.target.value;
    if (selectedPage && draftTitle !== selectedPage.title) dirty = true;
  }

  // TOC click → find the matching heading in the rendered ProseMirror DOM
  // and scrollIntoView. We match by text slug so the lookup works even
  // though Milkdown doesn't stamp heading IDs onto the DOM. Updates the
  // URL hash so the deep link is copyable.
  function scrollToHeading(heading) {
    const root = document.querySelector('.editor-frame .ProseMirror');
    if (!root) return;
    const nodes = root.querySelectorAll('h1, h2, h3, h4, h5, h6');
    for (const node of nodes) {
      if (slugify(node.textContent || '') === heading.slug) {
        node.scrollIntoView({ behavior: 'smooth', block: 'start' });
        // Replace, not push: TOC navigation inside a single page shouldn't
        // pollute history.
        try {
          history.replaceState(null, '', `#${heading.slug}`);
        } catch (_) {
          // Ignore SecurityError in sandboxed previews.
        }
        return;
      }
    }
  }

  // After a page loads and the editor renders, honor any hash in the URL
  // by scrolling to the matching heading once. Subsequent navigations
  // within the page are handled by scrollToHeading directly.
  $effect(() => {
    if (!selectedPage || loadingPage) return;
    const hash = window.location.hash?.slice(1);
    if (!hash) return;
    const target = headings.find((h) => h.slug === hash);
    if (!target) return;
    // Wait a tick for the editor's lazy import + render to settle.
    const handle = setTimeout(() => scrollToHeading(target), 100);
    return () => clearTimeout(handle);
  });
</script>

<div class="pages-view">
  <aside class="page-tree">
    <header class="tree-header">
      <h2>Pages</h2>
      <form
        class="new-page-form"
        onsubmit={(e) => {
          e.preventDefault();
          createPage();
        }}
      >
        <input
          id="page-new-title"
          type="text"
          placeholder={selectedPage ? `Child of ${selectedPage.title}` : 'New root page'}
          bind:value={newTitle}
          disabled={creating}
        />
        <button id="page-create-button" type="submit" disabled={creating || !newTitle.trim()}>
          New
        </button>
      </form>
    </header>

    {#if loadingTree}
      <p class="status">Loading…</p>
    {:else if pages.length === 0}
      <p class="status empty">No pages yet — create the first one above.</p>
    {:else}
      <ul class="tree" data-testid="page-tree">
        {#each pages as page (page.id)}
          <li
            class="tree-item"
            data-testid="page-tree-item"
            data-page-id={page.id}
            style="padding-left: {0.5 + page.depth * 0.8}rem"
          >
            <button
              type="button"
              class:active={selectedId === page.id}
              onclick={() => selectPage(page.id)}
            >
              {page.title}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </aside>

  <main class="page-pane">
    {#if error}
      <div class="error" role="alert">{error}</div>
    {/if}

    {#if !selectedPage && !loadingPage}
      <div class="empty-page">
        <h1>Knowledge Pages</h1>
        <p>Select a page from the tree, or create one to get started.</p>
      </div>
    {:else if loadingPage}
      <p class="status">Loading page…</p>
    {:else if selectedPage}
      <div class="toolbar">
        <input
          id="page-title-input"
          class="title-input"
          type="text"
          value={draftTitle}
          oninput={onTitleInput}
          placeholder="Untitled"
        />
        <div class="actions">
          <button
            id="page-save-button"
            type="button"
            onclick={savePage}
            disabled={!dirty || saving}
          >
            {saving ? 'Saving…' : 'Save'}
          </button>
          <button
            id="page-move-button"
            type="button"
            onclick={() => (moveDialogOpen = true)}
            disabled={saving}
          >
            Move
          </button>
          <button
            id="page-permissions-button"
            type="button"
            onclick={() => (permsDialogOpen = true)}
            disabled={saving}
          >
            Permissions
          </button>
          <button
            id="page-archive-button"
            type="button"
            class="danger"
            onclick={archivePage}
            disabled={saving}
          >
            Archive
          </button>
        </div>
      </div>
      <div class="editor-row">
        <div class="editor-frame" data-testid="page-editor">
          <LazyMilkdownEditor
            bind:content={draftContent}
            placeholder="Start writing…"
            showToolbar={true}
            entityType="page"
            entityId={selectedPage.id}
            onContentChange={onContentInput}
          />
        </div>
        {#if headings.length > 0}
          <aside class="toc" data-testid="page-toc" aria-label="Table of contents">
            <h3>On this page</h3>
            <ul>
              {#each headings as heading (heading.line)}
                <li
                  class="toc-item"
                  data-testid="page-toc-entry"
                  style="padding-left: {(heading.level - 1) * 0.75}rem"
                >
                  <button
                    type="button"
                    onclick={() => scrollToHeading(heading)}
                    title={heading.text}
                  >
                    {heading.text}
                  </button>
                </li>
              {/each}
            </ul>
          </aside>
        {/if}
      </div>
    {/if}
  </main>
</div>

{#if selectedPage}
  <PagePermissionsDialog
    bind:isOpen={permsDialogOpen}
    {workspaceId}
    pageId={selectedPage.id}
    onUpdated={loadTree}
  />
  <PageMoveDialog
    bind:isOpen={moveDialogOpen}
    {workspaceId}
    page={selectedPage}
    onMoved={async () => {
      await loadTree();
      await loadPage(selectedPage.id);
    }}
  />
{/if}

<style>
  .pages-view {
    display: grid;
    grid-template-columns: 280px 1fr;
    height: 100%;
    min-height: 0;
  }

  .page-tree {
    border-right: 1px solid var(--ds-border, #e5e7eb);
    overflow-y: auto;
    background: var(--ds-background-neutral, #fafafa);
  }

  .tree-header {
    padding: 1rem;
    border-bottom: 1px solid var(--ds-border, #e5e7eb);
  }

  .tree-header h2 {
    margin: 0 0 0.5rem 0;
    font-size: 0.875rem;
    font-weight: 600;
    text-transform: uppercase;
    color: var(--ds-text-subtle, #6b7280);
  }

  .new-page-form {
    display: flex;
    gap: 0.25rem;
  }

  .new-page-form input {
    flex: 1;
    padding: 0.25rem 0.5rem;
    font-size: 0.875rem;
    border: 1px solid var(--ds-border, #d1d5db);
    border-radius: 0.25rem;
    background: var(--ds-background, #fff);
    color: var(--ds-text, #111);
  }

  .new-page-form button {
    padding: 0.25rem 0.75rem;
    font-size: 0.875rem;
    border: 1px solid var(--ds-border, #d1d5db);
    border-radius: 0.25rem;
    background: var(--ds-background, #fff);
    cursor: pointer;
  }

  .tree {
    list-style: none;
    padding: 0.5rem 0;
    margin: 0;
  }

  .tree-item button {
    width: 100%;
    text-align: left;
    background: transparent;
    border: none;
    padding: 0.375rem 0.5rem;
    font-size: 0.875rem;
    color: var(--ds-text, #111);
    cursor: pointer;
    border-radius: 0.25rem;
  }

  .tree-item button:hover {
    background: var(--ds-background-neutral-hovered, #f3f4f6);
  }

  .tree-item button.active {
    background: var(--ds-background-selected, #e0f2fe);
    font-weight: 500;
  }

  .page-pane {
    overflow-y: auto;
    padding: 1.5rem 2rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
    min-height: 0;
  }

  .empty-page {
    color: var(--ds-text-subtle, #6b7280);
  }

  .toolbar {
    display: flex;
    gap: 1rem;
    align-items: center;
  }

  .title-input {
    flex: 1;
    font-size: 1.5rem;
    font-weight: 600;
    background: transparent;
    border: none;
    color: var(--ds-text, #111);
    outline: none;
  }

  .actions {
    display: flex;
    gap: 0.5rem;
  }

  .actions button {
    padding: 0.375rem 0.75rem;
    border: 1px solid var(--ds-border, #d1d5db);
    border-radius: 0.25rem;
    background: var(--ds-background, #fff);
    font-size: 0.875rem;
    cursor: pointer;
  }

  .actions button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .actions button.danger {
    color: var(--ds-text-danger, #b91c1c);
  }

  .editor-row {
    display: grid;
    grid-template-columns: 1fr 220px;
    gap: 1.5rem;
    flex: 1;
    min-height: 0;
  }

  .editor-frame {
    flex: 1;
    min-height: 300px;
    border: 1px solid var(--ds-border, #e5e7eb);
    border-radius: 0.375rem;
    overflow: hidden;
  }

  .toc {
    position: sticky;
    top: 0;
    align-self: start;
    max-height: calc(100vh - 8rem);
    overflow-y: auto;
    border-left: 1px solid var(--ds-border, #e5e7eb);
    padding-left: 1rem;
    font-size: 0.8125rem;
  }

  .toc h3 {
    margin: 0 0 0.5rem 0;
    font-size: 0.75rem;
    font-weight: 600;
    text-transform: uppercase;
    color: var(--ds-text-subtle, #6b7280);
  }

  .toc ul {
    list-style: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 0.125rem;
  }

  .toc button {
    background: transparent;
    border: none;
    padding: 0.25rem 0;
    text-align: left;
    color: var(--ds-text-subtle, #6b7280);
    cursor: pointer;
    width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .toc button:hover {
    color: var(--ds-text, #111);
  }

  @media (max-width: 1100px) {
    .editor-row {
      grid-template-columns: 1fr;
    }
    .toc {
      display: none;
    }
  }

  .error {
    padding: 0.75rem 1rem;
    background: var(--ds-background-danger, #fef2f2);
    color: var(--ds-text-danger, #b91c1c);
    border-radius: 0.25rem;
    font-size: 0.875rem;
  }

  .status {
    color: var(--ds-text-subtle, #6b7280);
    font-size: 0.875rem;
    padding: 1rem;
  }

  .status.empty {
    text-align: center;
    padding: 2rem 1rem;
  }
</style>
