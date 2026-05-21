<script>
  import { onMount } from 'svelte';
  import { api } from '../../api.js';
  import { navigate } from '../../router.js';
  import LazyMilkdownEditor from '../../editors/LazyMilkdownEditor.svelte';
  import PagePermissionsDialog from './PagePermissionsDialog.svelte';
  import PageMoveDialog from './PageMoveDialog.svelte';
  import { parseMarkdownHeadings, slugify } from './markdownToc.js';
  import Button from '../../components/Button.svelte';
  import DropdownMenu from '../../layout/DropdownMenu.svelte';
  import { IconBook as Book, IconDots as Dots } from '@tabler/icons-svelte-runes';
  import { confirm } from '../../composables/useConfirm.js';
  import { t } from '../../stores/i18n.svelte.js';
  import { pagesTreeRefresh } from './pagesTreeRefresh.svelte.js';
  import { pagesFocusTitle } from './pagesFocusTitle.svelte.js';

  /**
   * Workspace knowledge-pages view: right pane only (the tree + new-page
   * + actions live in PagesNavSidebar, which replaces the workspace
   * sidebar while the user is on a /pages route). Owns the title +
   * Markdown editor + the toolbar's Save button and ... menu.
   */
  let { workspaceId, pageId = null } = $props();

  let selectedId = $state(null);
  let selectedPage = $state(null);
  let draftTitle = $state('');
  let draftContent = $state('');
  let dirty = $state(false);
  let loadingPage = $state(false);
  let saving = $state(false);
  let error = $state('');
  let permsDialogOpen = $state(false);
  let moveDialogOpen = $state(false);
  let titleInputEl = $state(null);

  let headings = $derived(parseMarkdownHeadings(draftContent));

  onMount(async () => {
    if (pageId) {
      selectedId = pageId;
      await loadPage(pageId);
    }
  });

  // Sync to route changes — navigating to a different page id loads it;
  // navigating back to the bare /pages route clears the selection.
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

  // Honor a focus-title request from the sidebar (after + creates a new
  // page or "Rename" is picked from the kebab). Watch the tick so
  // repeated requests for the same page still re-focus.
  $effect(() => {
    pagesFocusTitle.tick;
    if (!pagesFocusTitle.pageId) return;
    if (!selectedPage || pagesFocusTitle.pageId !== selectedPage.id) return;
    if (!titleInputEl) return;
    titleInputEl.focus();
    titleInputEl.select();
    pagesFocusTitle.clear();
  });

  async function loadPage(id) {
    loadingPage = true;
    error = '';
    try {
      selectedPage = await api.pages.getPage(workspaceId, id);
      draftTitle = selectedPage.title;
      draftContent = selectedPage.content;
      dirty = false;
    } catch (err) {
      error = err?.message || t('pages.errorLoadPage');
      selectedPage = null;
    } finally {
      loadingPage = false;
    }
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
      pagesTreeRefresh.bump();
    } catch (err) {
      error = err?.message || t('pages.errorSave');
    } finally {
      saving = false;
    }
  }

  async function archivePage() {
    if (!selectedPage) return;
    const ok = await confirm({
      title: t('pages.archiveTitle', { title: selectedPage.title }),
      message: t('pages.archiveMessage'),
      confirmText: t('pages.archiveConfirm'),
      cancelText: t('common.cancel'),
      variant: 'danger',
    });
    if (!ok) return;
    try {
      await api.pages.archivePage(workspaceId, selectedPage.id);
      selectedPage = null;
      pagesTreeRefresh.bump();
      navigate(`/workspaces/${workspaceId}/pages`);
    } catch (err) {
      error = err?.message || t('pages.errorArchive');
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

  function onTitleBlur() {
    // Persist title on blur when changed — saves a click for the common
    // "rename and click away" interaction without surprising users on
    // pure-content edits (those still need the Save button).
    if (selectedPage && dirty && draftTitle !== selectedPage.title) {
      savePage();
    }
  }

  function onTitleKeydown(event) {
    if (event.key === 'Enter') {
      event.preventDefault();
      event.currentTarget.blur();
    }
  }

  let toolbarMenuItems = $derived([
    {
      id: 'move',
      type: 'regular',
      title: t('pages.menuMove'),
      onClick: () => (moveDialogOpen = true),
    },
    {
      id: 'permissions',
      type: 'regular',
      title: t('pages.menuPermissions'),
      onClick: () => (permsDialogOpen = true),
    },
    { id: 'divider', type: 'divider' },
    {
      id: 'archive',
      type: 'regular',
      title: t('pages.menuArchive'),
      color: 'var(--ds-text-danger)',
      onClick: archivePage,
    },
  ]);

  // TOC click → find the matching heading in the rendered ProseMirror DOM
  // and scrollIntoView. Match by text slug so the lookup works even though
  // Milkdown doesn't stamp heading IDs onto the DOM.
  function scrollToHeading(heading) {
    const root = document.querySelector('.editor-frame .ProseMirror');
    if (!root) return;
    const nodes = root.querySelectorAll('h1, h2, h3, h4, h5, h6');
    for (const node of nodes) {
      if (slugify(node.textContent || '') === heading.slug) {
        node.scrollIntoView({ behavior: 'smooth', block: 'start' });
        try {
          history.replaceState(null, '', `#${heading.slug}`);
        } catch (_) {
          /* ignore SecurityError in sandboxed previews */
        }
        return;
      }
    }
  }

  $effect(() => {
    if (!selectedPage || loadingPage) return;
    const hash = window.location.hash?.slice(1);
    if (!hash) return;
    const target = headings.find((h) => h.slug === hash);
    if (!target) return;
    const handle = setTimeout(() => scrollToHeading(target), 100);
    return () => clearTimeout(handle);
  });
</script>

<main class="page-pane">
  {#if error}
    <div class="error" role="alert">{error}</div>
  {/if}

  {#if !selectedPage && !loadingPage}
    <div class="empty-page">
      <Book size={48} color="var(--ds-text-subtle)" />
      <h1>{t('pages.emptyPaneTitle')}</h1>
      <p>{t('pages.emptyPaneDescription')}</p>
    </div>
  {:else if loadingPage}
    <p class="status">{t('pages.pageLoading')}</p>
  {:else if selectedPage}
    <div class="toolbar">
      <input
        id="page-title-input"
        bind:this={titleInputEl}
        class="title-input"
        type="text"
        value={draftTitle}
        oninput={onTitleInput}
        onblur={onTitleBlur}
        onkeydown={onTitleKeydown}
        placeholder={t('pages.titlePlaceholder')}
      />
      <div class="actions">
        <Button
          id="page-save-button"
          variant="primary"
          size="small"
          onclick={savePage}
          disabled={!dirty || saving}
          loading={saving}
        >
          {t('pages.save')}
        </Button>
        <DropdownMenu
          triggerIcon={Dots}
          items={toolbarMenuItems}
          showChevron={false}
          iconOnly={true}
          placement="bottom-end"
          triggerClass="toolbar-kebab"
          triggerTestid="page-toolbar-kebab"
        />
      </div>
    </div>
    <div class="editor-row">
      <div class="editor-frame" data-testid="page-editor">
        <LazyMilkdownEditor
          bind:content={draftContent}
          placeholder={t('pages.editorPlaceholder')}
          showToolbar={true}
          entityType="page"
          entityId={selectedPage.id}
          onContentChange={onContentInput}
        />
      </div>
      {#if headings.length > 0}
        <aside class="toc" data-testid="page-toc" aria-label={t('pages.tocAriaLabel')}>
          <h3>{t('pages.tocHeading')}</h3>
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

{#if selectedPage}
  <PagePermissionsDialog
    bind:isOpen={permsDialogOpen}
    {workspaceId}
    pageId={selectedPage.id}
    onUpdated={() => pagesTreeRefresh.bump()}
  />
  <PageMoveDialog
    bind:isOpen={moveDialogOpen}
    {workspaceId}
    page={selectedPage}
    onMoved={async () => {
      pagesTreeRefresh.bump();
      if (selectedPage) await loadPage(selectedPage.id);
    }}
  />
{/if}

<style>
  .page-pane {
    height: 100%;
    overflow-y: auto;
    padding: 1.5rem 2rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
    min-height: 0;
  }

  .empty-page {
    color: var(--ds-text-subtle);
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 0.5rem;
    padding: 2rem 0;
  }

  .empty-page h1 {
    font-size: 1.25rem;
    margin: 0.5rem 0 0.25rem;
    color: var(--ds-text);
  }

  .empty-page p {
    margin: 0;
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
    color: var(--ds-text);
    outline: none;
  }

  .actions {
    display: flex;
    gap: 0.5rem;
    align-items: center;
  }

  :global(.toolbar-kebab) {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    border-radius: 0.25rem;
    border: 1px solid var(--ds-border);
    background: var(--ds-surface);
    color: var(--ds-text-subtle);
    cursor: pointer;
  }

  :global(.toolbar-kebab:hover) {
    background: var(--ds-background-neutral-hovered);
    color: var(--ds-text);
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
    border: 1px solid var(--ds-border);
    border-radius: 0.375rem;
    overflow: hidden;
  }

  .toc {
    position: sticky;
    top: 0;
    align-self: start;
    max-height: calc(100vh - 8rem);
    overflow-y: auto;
    border-left: 1px solid var(--ds-border);
    padding-left: 1rem;
    font-size: 0.8125rem;
  }

  .toc h3 {
    margin: 0 0 0.5rem 0;
    font-size: 0.75rem;
    font-weight: 600;
    text-transform: uppercase;
    color: var(--ds-text-subtle);
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
    color: var(--ds-text-subtle);
    cursor: pointer;
    width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .toc button:hover {
    color: var(--ds-text);
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
    background: var(--ds-status-danger-bg);
    color: var(--ds-text-danger);
    border-radius: 0.25rem;
    font-size: 0.875rem;
  }

  .status {
    color: var(--ds-text-subtle);
    font-size: 0.875rem;
    padding: 1rem;
  }
</style>
