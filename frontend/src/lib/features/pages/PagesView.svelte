<script>
  import { onMount, onDestroy } from 'svelte';
  import { api } from '../../api.js';
  import { navigate } from '../../router.js';
  import LazyMilkdownEditor from '../../editors/LazyMilkdownEditor.svelte';
  import PagePermissionsDialog from './PagePermissionsDialog.svelte';
  import PageMoveDialog from './PageMoveDialog.svelte';
  import { parseMarkdownHeadings, slugify } from './markdownToc.js';
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
   * Markdown editor and the toolbar's `...` menu. Saves auto-flush on
   * a short debounce — there is no explicit Save button.
   */
  let { workspaceId, pageId = null } = $props();

  // Wait this long after the last keystroke before pushing the save.
  // 1.2s is long enough to coalesce a burst of typing into a single
  // request but short enough that a user who alt-tabs mid-sentence
  // won't lose noticeable work.
  const AUTOSAVE_DEBOUNCE_MS = 1200;

  let selectedId = $state(null);
  let selectedPage = $state(null);
  let draftTitle = $state('');
  let draftContent = $state('');
  let dirty = $state(false);
  let loadingPage = $state(false);
  let error = $state('');
  let permsDialogOpen = $state(false);
  let moveDialogOpen = $state(false);
  let titleInputEl = $state(null);

  // 'idle' = nothing to save; 'pending' = waiting for the debounce
  // timer; 'saving' = request in flight; 'saved' = last write
  // succeeded; 'error' = last write failed.
  let saveStatus = $state('idle');
  let saveTimer = null;

  let headings = $derived(parseMarkdownHeadings(draftContent));

  onMount(async () => {
    if (pageId) {
      selectedId = pageId;
      await loadPage(pageId);
    }
  });

  onDestroy(() => {
    // Don't leave a dangling timer. We deliberately do NOT flush here:
    // onDestroy fires during navigation away from the whole view, and
    // an in-flight save against the old workspace id would race with
    // the new view's setup. The route-change effect handles the
    // in-app flush; for tab close the user already saw "Saved" or
    // accepts the (small) loss within the debounce window.
    if (saveTimer) clearTimeout(saveTimer);
  });

  // Sync to route changes — navigating to a different page id loads
  // it; navigating back to the bare /pages clears the selection.
  // Flush any pending edits to the old page first so the user doesn't
  // lose mid-debounce work when jumping between pages.
  $effect(() => {
    if (pageId === selectedId) return;
    if (dirty) flushSave();
    selectedId = pageId;
    if (pageId) {
      loadPage(pageId);
    } else {
      selectedPage = null;
      draftTitle = '';
      draftContent = '';
      dirty = false;
      saveStatus = 'idle';
    }
  });

  // Honor a focus-title request from the sidebar (after + creates a
  // new page or "Rename" is picked from the kebab). Watch the tick so
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
      saveStatus = 'idle';
    } catch (err) {
      error = err?.message || t('pages.errorLoadPage');
      selectedPage = null;
    } finally {
      loadingPage = false;
    }
  }

  function scheduleAutoSave() {
    if (!selectedPage) return;
    saveStatus = 'pending';
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(() => {
      saveTimer = null;
      flushSave();
    }, AUTOSAVE_DEBOUNCE_MS);
  }

  /**
   * Push the current draft to the server immediately. Captures the
   * page id + draft contents up front so a concurrent navigation
   * (which would reassign selectedPage to a different row) cannot let
   * the response overwrite the wrong page's local state.
   */
  async function flushSave() {
    if (!selectedPage || !dirty) return;
    if (saveTimer) {
      clearTimeout(saveTimer);
      saveTimer = null;
    }
    const targetId = selectedPage.id;
    const titleSnap = draftTitle;
    const contentSnap = draftContent;
    saveStatus = 'saving';
    error = '';
    try {
      const updated = await api.pages.updatePage(workspaceId, targetId, {
        title: titleSnap,
        content: contentSnap,
      });
      // Only fold the response back into local state if we're still on
      // the same page; a fast-switching user has already moved on.
      if (selectedPage?.id === targetId) {
        selectedPage = updated;
        draftTitle = updated.title;
        draftContent = updated.content;
        dirty = false;
        saveStatus = 'saved';
      }
      pagesTreeRefresh.bump();
    } catch (err) {
      saveStatus = 'error';
      error = err?.message || t('pages.errorSave');
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
      // Drop any pending autosave for a page that no longer exists.
      if (saveTimer) {
        clearTimeout(saveTimer);
        saveTimer = null;
      }
      selectedPage = null;
      dirty = false;
      saveStatus = 'idle';
      pagesTreeRefresh.bump();
      navigate(`/workspaces/${workspaceId}/pages`);
    } catch (err) {
      error = err?.message || t('pages.errorArchive');
    }
  }

  function onContentInput(value) {
    if (!selectedPage) return;
    draftContent = value;
    if (value !== selectedPage.content || draftTitle !== selectedPage.title) {
      dirty = true;
      scheduleAutoSave();
    }
  }

  function onTitleInput(event) {
    if (!selectedPage) return;
    draftTitle = event.target.value;
    if (draftTitle !== selectedPage.title) {
      dirty = true;
      scheduleAutoSave();
    }
  }

  function onTitleKeydown(event) {
    if (event.key === 'Enter') {
      // Move focus into the body so the user can start typing right
      // away after committing the title.
      event.preventDefault();
      const editor = document.querySelector('.editor-frame .ProseMirror');
      if (editor instanceof HTMLElement) editor.focus();
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

  let statusLabel = $derived.by(() => {
    switch (saveStatus) {
      case 'saving':
        return t('pages.statusSaving');
      case 'pending':
        return t('pages.statusUnsaved');
      case 'saved':
        return t('pages.statusSaved');
      case 'error':
        return t('pages.statusError');
      default:
        return '';
    }
  });

  // TOC click → find the matching heading in the rendered ProseMirror
  // DOM and scrollIntoView. Match by text slug so the lookup works
  // even though Milkdown doesn't stamp heading IDs onto the DOM.
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
    <div class="page-frame">
      <div class="toolbar">
        <input
          id="page-title-input"
          bind:this={titleInputEl}
          class="title-input"
          type="text"
          value={draftTitle}
          oninput={onTitleInput}
          onkeydown={onTitleKeydown}
          placeholder={t('pages.titlePlaceholder')}
        />
        <div class="actions">
          {#if statusLabel}
            <span
              class="save-status"
              class:save-status--error={saveStatus === 'error'}
              data-testid="page-save-status"
              data-status={saveStatus}
            >
              {statusLabel}
            </span>
          {/if}
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
    padding: 1.5rem 0;
    display: flex;
    flex-direction: column;
    min-height: 0;
  }

  .page-frame {
    width: 100%;
    /* No max-width — use the full pane. The right-hand TOC column
       takes its own 220px slot in the editor-row, so the editor still
       has a natural right margin when headings exist. */
    margin: 0 auto;
    padding: 0 3rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
    flex: 1;
    min-height: 0;
  }

  .empty-page {
    color: var(--ds-text-subtle);
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 0.5rem;
    padding: 2rem 3rem;
    max-width: 880px;
    margin: 0 auto;
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
    font-size: 2rem;
    font-weight: 700;
    line-height: 1.2;
    background: transparent;
    border: none;
    color: var(--ds-text);
    outline: none;
    padding: 0;
  }

  .actions {
    display: flex;
    gap: 0.5rem;
    align-items: center;
  }

  .save-status {
    font-size: 0.75rem;
    color: var(--ds-text-subtle);
    /* Reserve space so the kebab doesn't jitter as the label
       transitions between Saving… / Saved / Unsaved. */
    min-width: 4rem;
    text-align: right;
  }

  .save-status--error {
    color: var(--ds-text-danger);
  }

  :global(.toolbar-kebab) {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2rem;
    height: 2rem;
    border-radius: 0.25rem;
    border: none;
    background: transparent;
    color: var(--ds-text-subtle);
    cursor: pointer;
  }

  :global(.toolbar-kebab:hover) {
    background: var(--ds-background-neutral-hovered);
    color: var(--ds-text);
  }

  /* Single-column when no TOC; two-column only when there are
     headings to show. Grid with a reserved 220px right column left
     dead space below the editor frame, even when the TOC wasn't
     rendered. */
  .editor-row {
    display: flex;
    flex-direction: row;
    gap: 2rem;
    flex: 1;
    min-height: 0;
  }

  /* Frameless editor: no border, no rounded corners, no background —
     content sits flush with the page like Docmost. The flex chain
     below propagates height all the way down to .ProseMirror so a
     click anywhere in the empty space lands the cursor at the end of
     the document. */
  .editor-frame {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }

  /* The embedded MilkdownEditor ships its own border + tinted card +
     150px min-height that paint as a small box. Inside the pages
     surface we strip the card and stretch every wrapper in the chain
     so the ProseMirror surface fills the whole page-pane.
     ----------------------------------------------------------------
     Specificity note: MilkdownEditor's scoped CSS produces selectors
     like `.milkdown-toolbar.svelte-HASH` (specificity 0,2,0). A plain
     `.editor-frame .milkdown-toolbar` ties at 0,2,0 and loses on
     source order because the dynamically-imported MilkdownEditor
     bundle is appended to <head> AFTER PagesView's stylesheet. Every
     override below chains through `.milkdown-wrapper` (or doubles the
     `.editor-frame` class) to lift specificity above the scoped
     originals; without that, the cascade tie silently restores the
     bordered card. Scope stays inside `.editor-frame` so inline
     editors and item descriptions keep their boxed look. */
  :global(.editor-frame .milkdown-wrapper) {
    flex: 1;
    display: flex;
    flex-direction: column;
    min-height: 0;
    /* Flatten the wrapper's own rounded card so the editor disappears
       into the page background regardless of focus state. */
    border-radius: 0;
    overflow: visible;
  }

  /* Kill the 2px blue focus halo MilkdownEditor draws on the wrapper
     for inline contexts. Doubled class bumps specificity above the
     scoped `.milkdown-wrapper:focus-within.svelte-HASH` (0,3,0). */
  :global(.editor-frame.editor-frame .milkdown-wrapper:focus-within) {
    box-shadow: none;
  }

  :global(.editor-frame .milkdown-wrapper .milkdown-editor),
  :global(.editor-frame .milkdown-wrapper .milkdown-editor.has-toolbar) {
    flex: 1;
    display: flex;
    flex-direction: column;
    border: none;
    border-radius: 0;
    background: transparent;
    overflow: visible;
  }

  /* Confluence-style toolbar: floats on the page background with a
     single hairline beneath it instead of being a tinted card top. */
  :global(.editor-frame .milkdown-wrapper .milkdown-toolbar) {
    border: none;
    border-radius: 0;
    background: transparent;
    padding: 0 0 0.375rem 0;
    border-bottom: 1px solid var(--ds-border);
  }

  :global(.editor-frame .milkdown-wrapper .milkdown-editor .milkdown) {
    flex: 1;
    min-height: 0;
    /* Breathing room between the toolbar's divider and the first
       paragraph; otherwise the body sits flush against the hairline. */
    padding: 1.5rem 0 0 0;
    display: flex;
    flex-direction: column;
  }

  /* ProseMirror itself must grow so the entire empty column is
     clickable + focusable, not just the first text node. */
  :global(.editor-frame .milkdown-wrapper .milkdown-editor .ProseMirror) {
    flex: 1;
    min-height: 50vh;
    outline: none;
  }

  .toc {
    flex: 0 0 220px;
    position: sticky;
    top: 0;
    align-self: start;
    max-height: calc(100vh - 8rem);
    overflow-y: auto;
    padding-left: 1rem;
    border-left: 1px solid var(--ds-border);
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
    margin: 0 auto 1rem;
    max-width: 880px;
    padding: 0.75rem 1rem;
    background: var(--ds-status-danger-bg);
    color: var(--ds-text-danger);
    border-radius: 0.25rem;
    font-size: 0.875rem;
  }

  .status {
    color: var(--ds-text-subtle);
    font-size: 0.875rem;
    padding: 1rem 3rem;
    max-width: 880px;
    margin: 0 auto;
  }
</style>
