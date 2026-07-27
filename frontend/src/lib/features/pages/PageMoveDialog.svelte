<script>
  import Modal from '../../dialogs/Modal.svelte';
  import ModalHeader from '../../dialogs/ModalHeader.svelte';
  import DialogFooter from '../../dialogs/DialogFooter.svelte';
  import BasePicker from '../../pickers/BasePicker.svelte';
  import { api } from '../../api.js';
  import { t } from '../../stores/i18n.svelte.js';

  /** Page reparenting dialog. It excludes self, descendants, and current parent
   * from tree-path candidates while the backend remains the cycle authority. */
  let {
    isOpen = $bindable(false),
    workspaceId,
    page,
    onMoved = null,
  } = $props();

  let candidates = $state([]);
  let loading = $state(false);
  let saving = $state(false);
  let error = $state('');

  /** Root and no selection both bind as null, so selectionMade disambiguates. */
  let pickedParentId = $state(null);
  let selectionMade = $state(false);

  $effect(() => {
    if (isOpen && page) {
      loadCandidates();
    }
    if (!isOpen) {
      pickedParentId = null;
      selectionMade = false;
      error = '';
    }
  });

  async function loadCandidates() {
    loading = true;
    error = '';
    try {
      const resp = await api.pages.getTree(workspaceId);
      const all = resp.pages || [];
      // A page p2 is a descendant of `page` iff its materialized path
      // starts with the path that the children of `page` would have.
      // Schema path format: "/a/b/c/" — descendants of page id N at path
      // "/X/N/" have paths starting with "/X/N/".
      const selfPrefix = `${page.path}${page.id}/`;
      candidates = all.filter((p) => {
        if (p.id === page.id) return false;
        if (p.path === selfPrefix || p.path.startsWith(selfPrefix)) return false;
        if (p.id === page.parent_id) return false; // already the parent
        return true;
      });
    } catch (err) {
      error = err?.message || t('pages.errorLoadTree');
    } finally {
      loading = false;
    }
  }

  function onPick(item) {
    // BasePicker fires onSelect with the chosen item, or null when the
    // user picks the "Workspace root" (showUnassigned) option.
    selectionMade = true;
    pickedParentId = item ? item.id : null;
  }

  // Show the "Workspace root" option only when moving there would
  // actually change something. If the page is already at the root it'd
  // be a confusing no-op.
  const rootAvailable = $derived(page?.parent_id != null);

  async function confirmMove() {
    if (!selectionMade || saving) return;
    saving = true;
    error = '';
    try {
      await api.pages.movePage(workspaceId, page.id, pickedParentId);
      isOpen = false;
      onMoved?.();
    } catch (err) {
      error = err?.message || t('pages.errorMove');
    } finally {
      saving = false;
    }
  }
</script>

<Modal bind:isOpen maxWidth="max-w-lg" onSubmit={confirmMove} submitDisabled={!selectionMade || saving}>
  <ModalHeader
    title={t('pages.moveTitle', { title: page?.title || '' })}
    subtitle={t('pages.moveSubtitle')}
    onClose={() => (isOpen = false)}
  />
  <div class="dialog">
    {#if error}
      <div class="error" role="alert">{error}</div>
    {/if}

    <BasePicker
      id="page-move-picker"
      bind:value={pickedParentId}
      items={candidates}
      {loading}
      placeholder={t('pages.moveSearchPlaceholder')}
      showUnassigned={rootAvailable}
      unassignedLabel={t('pages.moveRoot')}
      searchFields={['title', 'path']}
      getValue={(p) => p.id}
      getLabel={(p) => p.title}
      onSelect={onPick}
    />
  </div>
  <DialogFooter
    cancelLabel={t('pages.moveCancel')}
    confirmLabel={t('pages.moveButton')}
    confirmTestid="page-move-confirm"
    cancelTestid="page-move-cancel"
    confirmDisabled={!selectionMade}
    loading={saving}
    showKeyboardHint={true}
    onCancel={() => (isOpen = false)}
    onConfirm={confirmMove}
  />
</Modal>

<style>
  .dialog {
    padding: 1rem 1.25rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .error {
    padding: 0.625rem 0.875rem;
    background: var(--ds-status-danger-bg, #fef2f2);
    color: var(--ds-text-danger, #b91c1c);
    border-radius: 0.25rem;
    font-size: 0.875rem;
  }
</style>
