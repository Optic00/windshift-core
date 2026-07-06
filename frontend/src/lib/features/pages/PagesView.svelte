<script>
  import { onMount, onDestroy } from 'svelte';
  import { api } from '../../api.js';
  import { navigate } from '../../router.js';
  import LazyMilkdownEditor from '../../editors/LazyMilkdownEditor.svelte';
  import PagePermissionsDialog from './PagePermissionsDialog.svelte';
  import PageMoveDialog from './PageMoveDialog.svelte';
  import PagesHistoryDrawer from './PagesHistoryDrawer.svelte';
  import PageLabelPicker from './PageLabelPicker.svelte';
  import PageWorkItemsButton from './PageWorkItemsButton.svelte';
  import IconSelector from '../../pickers/IconSelector.svelte';
  import { workspaceIconMap } from '../../utils/icons.js';
  import { IconX } from '@tabler/icons-svelte-runes';
  import { parseMarkdownHeadings, slugify } from './markdownToc.js';
  import DropdownMenu from '../../layout/DropdownMenu.svelte';
  import {
    IconBook as Book,
    IconDots as Dots,
    IconPencil as Pencil,
    IconEye as Eye,
  } from '@tabler/icons-svelte-runes';
  import { confirm } from '../../composables/useConfirm.js';
  import { t } from '../../stores/i18n.svelte.js';
  import { pagesTreeRefresh } from './pagesTreeRefresh.svelte.js';
  import { pagesFocusTitle } from './pagesFocusTitle.svelte.js';
  import { agentRuns } from '../../stores/agentRuns.svelte.js';

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
  // Monotonic token for loadPage. A user who clicks page A then quickly
  // page B can get the slow A response back after B's fast response —
  // without this guard, the late A response would clobber selectedPage and
  // the editor would silently swap to A's content. The token also lets the
  // route-clearing branch invalidate any in-flight load.
  let loadPageRequestSeq = 0;
  let permsDialogOpen = $state(false);
  let moveDialogOpen = $state(false);
  let historyDrawerOpen = $state(false);
  let titleInputEl = $state(null);
  let pageEffectiveLevel = $state('');
  let appearanceSaving = $state(false);
  let pickerIcon = $state('Plus');
  let pickerColor = $state('#3b82f6');
  let PageTitleIcon = $derived(selectedPage?.metadata?.icon ? workspaceIconMap[selectedPage.metadata.icon] : null);
  let pageTitleIconColor = $derived(selectedPage?.metadata?.color || 'var(--ds-text-subtle)');

  // 'idle' = nothing to save; 'pending' = waiting for the debounce
  // timer; 'saving' = request in flight; 'saved' = last write
  // succeeded; 'error' = last write failed.
  let saveStatus = $state('idle');
  let saveTimer = null;
  // Guards flushSave against re-entry while a save is awaiting the
  // server. Without this, a debounce that fires during an in-flight
  // request would issue a second concurrent save whose response can
  // arrive out of order and clobber state.
  let saveInFlight = false;

  // 'edit' (default) shows the formatting toolbar + lets the user type.
  // 'read' renders the body read-only, hides the toolbar (via
  // MilkdownEditor's own derivation on `readonly`), and surfaces the
  // table of contents on the right. Resets to 'edit' when the route
  // changes — writing-first default matches Confluence/Notion.
  let mode = $state('edit');

  let headings = $derived(parseMarkdownHeadings(draftContent));

  // Linked work items (other side of the item↔page links touching this
  // page). Loaded alongside the page itself; mutated optimistically by
  // the popover button's add/remove callbacks.
  let pageLinks = $state(/** @type {any[]} */ ([]));
  let loadingPageLinks = $state(false);
  // System link types — used to locate the "Page" type id so the popover
  // can pass it to POST /links. One fetch per session is plenty; the
  // result is small and stable.
  let linkTypesCache = $state(/** @type {any[]} */ ([]));
  let pageLinkTypeId = $derived(
    linkTypesCache.find((lt) => lt?.name === 'Page')?.id ?? null
  );

  async function loadPageLinks(id) {
    loadingPageLinks = true;
    try {
      const resp = await api.links.getForPage(id);
      const outgoing = Array.isArray(resp?.outgoing) ? resp.outgoing : [];
      const incoming = Array.isArray(resp?.incoming) ? resp.incoming : [];
      // De-dup by id; outgoing and incoming should never share rows but
      // belt-and-braces against backend changes.
      const seen = new Set();
      const merged = [];
      for (const link of [...incoming, ...outgoing]) {
        if (link && link.id != null && !seen.has(link.id)) {
          seen.add(link.id);
          merged.push(link);
        }
      }
      pageLinks = merged;
    } catch (err) {
      console.error('failed to load page links', err);
      pageLinks = [];
    } finally {
      loadingPageLinks = false;
    }
  }

  async function ensureLinkTypesLoaded() {
    if (linkTypesCache.length > 0) return;
    try {
      const resp = await api.linkTypes.getAll();
      linkTypesCache = Array.isArray(resp) ? resp : (resp?.data ?? []);
    } catch (err) {
      console.error('failed to load link types', err);
    }
  }

  function handlePageLinkCreated(link) {
    if (link && !pageLinks.some((l) => l.id === link.id)) {
      pageLinks = [link, ...pageLinks];
    }
    if (selectedPage) {
      // Re-fetch to pick up joined fields the server applies on read
      // (status_name, item_type_icon, workspace_key) that the POST
      // response doesn't include.
      loadPageLinks(selectedPage.id);
    }
  }

  function handlePageLinkRemoved(linkId) {
    pageLinks = pageLinks.filter((l) => l.id !== linkId);
  }

  onMount(async () => {
    if (pageId) {
      selectedId = pageId;
      await loadPage(pageId);
    }
  });

  // Live-reload after AI chat agent runs so page edits made through update_page
  // become visible without requiring a manual refresh. If the user currently
  // has local unsaved edits, skip the reload rather than clobber their draft.
  onMount(() => agentRuns.subscribe(() => {
    pagesTreeRefresh.bump();
    if (!selectedPage?.id || dirty || saveInFlight) return;
    loadPage(selectedPage.id);
  }));

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
    mode = 'edit';
    if (pageId) {
      loadPage(pageId);
    } else {
      // Bump the token so any in-flight loadPage can't write back into
      // the cleared state when its response eventually lands.
      loadPageRequestSeq++;
      selectedPage = null;
      draftTitle = '';
      draftContent = '';
      dirty = false;
      saveStatus = 'idle';
      pageLinks = [];
      pageEffectiveLevel = '';
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
    const requestSeq = ++loadPageRequestSeq;
    loadingPage = true;
    error = '';
    try {
      const page = await api.pages.getPage(workspaceId, id);
      if (requestSeq !== loadPageRequestSeq) return;
      selectedPage = page;
      draftTitle = page.title;
      draftContent = page.content;
      pickerIcon = page.metadata?.icon || 'Plus';
      pickerColor = page.metadata?.color || '#3b82f6';
      dirty = false;
      saveStatus = 'idle';
      pageEffectiveLevel = '';
      // Run in parallel: linked work items / permissions are independent
      // of the page payload, and the link-types list is cached for the session.
      void loadPageLinks(id);
      void ensurePageEffectiveLevel(id);
      void ensureLinkTypesLoaded();
    } catch (err) {
      if (requestSeq !== loadPageRequestSeq) return;
      error = err?.message || t('pages.errorLoadPage');
      selectedPage = null;
      pageEffectiveLevel = '';
    } finally {
      if (requestSeq === loadPageRequestSeq) loadingPage = false;
    }
  }

  async function ensurePageEffectiveLevel(id) {
    try {
      const perms = await api.pages.getPermissions(workspaceId, id);
      if (selectedPage?.id === id) {
        pageEffectiveLevel = perms?.effective_level || '';
      }
    } catch (err) {
      console.error('failed to load page permissions', err);
      if (selectedPage?.id === id) pageEffectiveLevel = '';
    }
  }

  // Label chip helpers — attach/detach mutate the local selectedPage.labels
  // optimistically so the chip row reflects state instantly; on error we
  // roll back and surface the failure in the error banner.
  let selectedLabelIds = $derived(
    new Set((selectedPage?.labels || []).map((l) => l.id))
  );

  async function onLabelToggle(label) {
    if (!selectedPage) return;
    const isAttached = selectedLabelIds.has(label.id);
    const previous = selectedPage.labels || [];
    try {
      if (isAttached) {
        selectedPage.labels = previous.filter((l) => l.id !== label.id);
        await api.pageLabels.removeFromPage(workspaceId, selectedPage.id, label.id);
      } else {
        selectedPage.labels = [...previous, label].sort((a, b) =>
          a.name.localeCompare(b.name)
        );
        await api.pageLabels.addToPage(workspaceId, selectedPage.id, label.id);
      }
      pagesTreeRefresh.bump();
    } catch (err) {
      selectedPage.labels = previous;
      error = err?.message || t(isAttached ? 'pages.labelsErrorDetach' : 'pages.labelsErrorAttach');
    }
  }

  async function removeLabel(label) {
    await onLabelToggle(label);
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
   * the response overwrite the wrong page's local state, and so a
   * slow response cannot clobber keystrokes typed during the in-flight
   * window — we only fold the response back into draftTitle /
   * draftContent when the draft is still identical to what we sent.
   */
  async function flushSave() {
    if (!selectedPage || !dirty) return;
    // Single-flight: a debounce that fires while a save is already
    // in flight should defer rather than race. The pending input has
    // already kept dirty=true, so the post-request reschedule below
    // will pick it up.
    if (saveInFlight) return;
    if (saveTimer) {
      clearTimeout(saveTimer);
      saveTimer = null;
    }
    const targetId = selectedPage.id;
    const prevTitle = selectedPage.title;
    const titleSnap = draftTitle;
    const contentSnap = draftContent;
    saveStatus = 'saving';
    error = '';
    saveInFlight = true;
    try {
      const updated = await api.pages.updatePage(workspaceId, targetId, {
        title: titleSnap,
        content: contentSnap,
      });
      // Only fold the response back into local state if we're still on
      // the same page; a fast-switching user has already moved on.
      if (selectedPage?.id === targetId) {
        selectedPage = updated;
        // If the user kept typing while the request was in flight,
        // the local draft is newer than what the server just echoed
        // back. Preserve the newer draft and let the next debounce
        // ship it; only clear `dirty` when the snapshot we sent is
        // still the current draft.
        const stillClean =
          draftTitle === titleSnap && draftContent === contentSnap;
        if (stillClean) {
          draftTitle = updated.title;
          draftContent = updated.content;
          dirty = false;
          saveStatus = 'saved';
        } else {
          saveStatus = 'pending';
          scheduleAutoSave();
        }
      }
      // The sidebar shows only titles, so a content-only save needs no
      // sidebar update at all. Signal a targeted rename only when the title
      // actually changed — that patches one row in place instead of
      // refetching (and flashing) the whole tree.
      if (updated.title !== prevTitle) {
        pagesTreeRefresh.rename(updated.id, updated.title);
      }
    } catch (err) {
      saveStatus = 'error';
      error = err?.message || t('pages.errorSave');
    } finally {
      saveInFlight = false;
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

  async function updatePageAppearance({ icon = pickerIcon, color = pickerColor, clear = false } = {}) {
    if (!selectedPage || appearanceSaving) return;
    if (dirty && !saveInFlight) await flushSave();
    const previous = selectedPage.metadata || {};
    const metadata = { ...previous };
    if (clear) {
      delete metadata.icon;
      delete metadata.color;
    } else {
      metadata.icon = icon;
      metadata.color = color;
    }
    selectedPage.metadata = metadata;
    selectedPage = selectedPage;
    appearanceSaving = true;
    try {
      const updated = await api.pages.updatePage(workspaceId, selectedPage.id, {
        title: draftTitle,
        content: draftContent,
        metadata,
      });
      if (selectedPage?.id === updated.id) {
        selectedPage = { ...updated, content: draftContent };
        pickerIcon = updated.metadata?.icon || 'Plus';
        pickerColor = updated.metadata?.color || '#3b82f6';
      }
      pagesTreeRefresh.bump();
    } catch (err) {
      selectedPage.metadata = previous;
      selectedPage = selectedPage;
      error = err?.message || t('pages.errorSave');
    } finally {
      appearanceSaving = false;
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
    {
      id: 'history',
      type: 'regular',
      title: t('pages.menuHistory'),
      testid: 'page-menu-history',
      onClick: () => (historyDrawerOpen = true),
    },
    {
      id: 'print',
      type: 'regular',
      title: t('pages.menuPrint'),
      testid: 'page-menu-print',
      // Open the chrome-free print view in a new tab so the editor tab
      // (and any in-flight autosave) is left untouched; the print tab
      // auto-opens the browser print dialog once content has rendered.
      onClick: () =>
        window.open(
          `/workspaces/${workspaceId}/pages/${selectedPage.id}/print`,
          '_blank',
          'noopener'
        ),
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
        <div class="title-wrap">
          {#if PageTitleIcon}
            <PageTitleIcon
              size={22}
              class="title-icon"
              style="color: {pageTitleIconColor};"
              aria-hidden="true"
            />
          {/if}
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
        </div>
        <div class="actions">
          {#if statusLabel && mode === 'edit'}
            <span
              class="save-status"
              class:save-status--error={saveStatus === 'error'}
              data-testid="page-save-status"
              data-status={saveStatus}
            >
              {statusLabel}
            </span>
          {/if}
          {#if selectedPage}
            <PageWorkItemsButton
              workspaceId={selectedPage.workspace_id ?? workspaceId}
              pageId={selectedPage.id}
              pageLinks={pageLinks}
              loading={loadingPageLinks}
              pageLinkTypeId={pageLinkTypeId}
              onlinkCreated={handlePageLinkCreated}
              onlinkRemoved={handlePageLinkRemoved}
            />
          {/if}
          <div
            class="mode-toggle"
            role="group"
            aria-label={t('pages.modeAria')}
            data-testid="page-mode-toggle"
          >
            <button
              type="button"
              class="mode-toggle__btn"
              class:active={mode === 'edit'}
              aria-pressed={mode === 'edit'}
              onclick={() => (mode = 'edit')}
              data-testid="page-mode-edit"
            >
              <Pencil size={14} />
              <span>{t('pages.modeEdit')}</span>
            </button>
            <button
              type="button"
              class="mode-toggle__btn"
              class:active={mode === 'read'}
              aria-pressed={mode === 'read'}
              onclick={() => (mode = 'read')}
              data-testid="page-mode-read"
            >
              <Eye size={14} />
              <span>{t('pages.modeRead')}</span>
            </button>
          </div>
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
      <div class="label-row" data-testid="page-label-row">
        {#if mode === 'edit'}
          <div class="appearance-actions" aria-label="Page icon">
            <IconSelector
              bind:selectedIcon={pickerIcon}
              bind:selectedColor={pickerColor}
              compact
              hideLabel
              label=""
              triggerVariant="badge"
              triggerTitle={selectedPage.metadata?.icon ? 'Change page icon' : 'Add page icon'}
              onchange={(event) => updatePageAppearance(event.detail)}
            />
            {#if selectedPage.metadata?.icon}
              <button
                type="button"
                class="clear-icon-button"
                onclick={() => updatePageAppearance({ clear: true })}
                disabled={appearanceSaving}
                aria-label="Remove page icon"
                title="Remove page icon"
              >
                <IconX size={12} />
              </button>
            {/if}
          </div>
        {/if}
        {#each selectedPage.labels || [] as label (label.id)}
          <span
            class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs"
            style="background-color: {label.color || '#3B82F6'}1A; color: var(--ds-text); border: 1px solid {label.color || '#3B82F6'};"
            data-testid="page-label-chip"
            data-label-id={label.id}
          >
            <span
              class="inline-block w-2 h-2 rounded-full"
              style="background-color: {label.color || '#3B82F6'};"
              aria-hidden="true"
            ></span>
            {label.name}
            {#if mode === 'edit'}
              <button
                type="button"
                class="label-chip__remove"
                onclick={() => removeLabel(label)}
                aria-label={t('pages.labelsRemoveAria', { name: label.name })}
                data-testid="page-label-chip-remove"
              >
                <IconX size={12} />
              </button>
            {/if}
          </span>
        {/each}
        {#if mode === 'edit'}
          <PageLabelPicker
            {workspaceId}
            selectedIds={selectedLabelIds}
            onToggle={onLabelToggle}
            triggerLabel={t('pages.labelsAdd')}
          />
        {/if}
      </div>
      <div class="editor-row">
        <div class="editor-frame" data-testid="page-editor">
          <LazyMilkdownEditor
            bind:content={draftContent}
            placeholder={t('pages.editorPlaceholder')}
            showToolbar={true}
            readonly={mode === 'read'}
            entityType="page"
            entityId={selectedPage.id}
            enableDiagrams={true}
            {workspaceId}
            onContentChange={onContentInput}
          />
        </div>
        {#if mode === 'read' && headings.length > 0}
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
  <PagesHistoryDrawer
    bind:open={historyDrawerOpen}
    {workspaceId}
    pageId={selectedPage.id}
    canRestore={pageEffectiveLevel === 'edit' || pageEffectiveLevel === 'admin'}
    onRestored={async () => {
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
       has a natural right margin when headings exist.
       No horizontal padding either — that pad is pushed onto each
       content child below so the toolbar's bottom border can run
       edge-to-edge across the pane while the icons + body content
       remain visually inset. */
    margin: 0 auto;
    padding: 0;
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
    padding: 0 3rem;
  }

  .label-row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem;
    align-items: center;
    padding: 0 3rem;
    margin-top: -0.5rem;
  }

  /* Chip visuals (background/border/dot) come from inline Tailwind classes —
     same shape as the work-item label chip in ItemDetailSidebar so page
     labels look identical to work-item labels. Only the remove button gets
     a scoped style since Tailwind doesn't carry an opacity-on-hover utility
     cheaply here. */
  .label-chip__remove {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: none;
    color: inherit;
    cursor: pointer;
    padding: 0;
    opacity: 0.7;
    transition: opacity 120ms;
  }

  .label-chip__remove:hover {
    opacity: 1;
  }

  .title-wrap {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 0.625rem;
  }

  :global(.title-icon) {
    flex-shrink: 0;
  }

  .title-input {
    flex: 1;
    min-width: 0;
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

  .appearance-actions {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
  }

  .clear-icon-button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.5rem;
    height: 1.5rem;
    border: none;
    border-radius: 0.25rem;
    background: transparent;
    color: var(--ds-text-subtle);
    cursor: pointer;
  }

  .clear-icon-button:hover:not(:disabled) {
    background: var(--ds-background-neutral-hovered);
    color: var(--ds-text);
  }

  .clear-icon-button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
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

  /* Segmented Edit | Read toggle. Two buttons sharing a 1px border;
     active button uses --ds-surface-selected to read as pressed.
     Tab-able via Tab; aria-pressed tells screen readers which is on. */
  .mode-toggle {
    display: inline-flex;
    border: 1px solid var(--ds-border);
    border-radius: 0.25rem;
    overflow: hidden;
  }

  .mode-toggle__btn {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.25rem 0.5rem;
    background: transparent;
    border: none;
    color: var(--ds-text-subtle);
    font-size: 0.75rem;
    cursor: pointer;
  }

  .mode-toggle__btn + .mode-toggle__btn {
    border-left: 1px solid var(--ds-border);
  }

  .mode-toggle__btn:hover {
    background: var(--ds-background-neutral-hovered);
    color: var(--ds-text);
  }

  .mode-toggle__btn.active {
    background: var(--ds-surface-selected);
    color: var(--ds-text);
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
    /* No horizontal padding — the toolbar's bottom divider must hit
       both walls flush. The TOC carries its own right margin (below)
       so it doesn't kiss the right edge when present. */
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
     single hairline beneath it instead of being a tinted card top.
     The 3rem left inset matches the title above and the body below;
     the bottom border spans the full toolbar width (= full
     .editor-frame width), so the hairline visually extends edge-to-
     edge of the pane while the icons stay inset. */
  :global(.editor-frame .milkdown-wrapper .milkdown-toolbar) {
    border: none;
    border-radius: 0;
    background: transparent;
    padding: 0 3rem 0.375rem 3rem;
    border-bottom: 1px solid var(--ds-border);
  }

  :global(.editor-frame .milkdown-wrapper .milkdown-editor .milkdown) {
    flex: 1;
    min-height: 0;
    /* Top: breathing room below the toolbar divider.
       Left/right: keeps the prose inset 3rem so it aligns with the
       title above (the toolbar divider's left edge runs further out). */
    padding: 1.5rem 3rem 0 3rem;
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
    /* Inset from the pane's right wall. Carried here (instead of on
       .editor-row) so the editor's toolbar divider can run flush to
       the right edge in Edit mode when the TOC isn't shown. */
    margin-right: 3rem;
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
