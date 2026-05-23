<script>
  import Modal from '../../dialogs/Modal.svelte';
  import ModalHeader from '../../dialogs/ModalHeader.svelte';
  import DataTable from '../../components/DataTable.svelte';
  import Button from '../../components/Button.svelte';
  import { Archive } from '@lucide/svelte';
  import { api } from '../../api.js';
  import { t } from '../../stores/i18n.svelte.js';
  import { errorToast, successToast } from '../../stores/toasts.svelte.js';
  import { confirm } from '../../composables/useConfirm.js';
  import { formatDateSimple } from '../../utils/dateFormatter.js';

  /**
   * Admin-only modal listing every archived page in the workspace, with a
   * per-row Unarchive button. The backend's /pages/archived endpoint is
   * gated to system.admin or workspace.admin (404 otherwise) so even if a
   * non-admin reaches this component, the list comes back empty/404.
   *
   * The Unarchive action calls the dedicated POST /pages/{id}/unarchive
   * endpoint — distinct from "restore a revision" because we don't want
   * to overwrite content as a side effect. Single-page only: ancestors
   * stay archived unless explicitly unarchived too.
   */
  let { isOpen = $bindable(false), workspaceId, onUnarchived = () => {} } = $props();

  let rows = $state(/** @type {any[]} */ ([]));
  let loading = $state(false);
  let pendingId = $state(/** @type {number | null} */ (null));

  $effect(() => {
    if (isOpen && workspaceId) {
      void load();
    }
    if (!isOpen) {
      rows = [];
      pendingId = null;
    }
  });

  async function load() {
    loading = true;
    try {
      const data = await api.pages.listArchived(workspaceId);
      rows = Array.isArray(data) ? data : [];
    } catch (err) {
      errorToast(err?.message || t('pages.archivedLoadError'));
    } finally {
      loading = false;
    }
  }

  async function handleUnarchive(row) {
    const ok = await confirm({
      title: t('pages.archivedUnarchiveTitle', { title: row.title }),
      message: t('pages.archivedUnarchiveMessage'),
      confirmText: t('pages.archivedUnarchiveConfirm'),
      variant: 'primary',
    });
    if (!ok) return;
    pendingId = row.id;
    try {
      await api.pages.unarchive(workspaceId, row.id);
      successToast(t('pages.archivedUnarchiveOK', { title: row.title }));
      rows = rows.filter((r) => r.id !== row.id);
      onUnarchived();
    } catch (err) {
      errorToast(err?.message || t('pages.archivedUnarchiveError'));
    } finally {
      pendingId = null;
    }
  }

  const columns = $derived([
    { key: 'title', label: t('pages.archivedColTitle'), sortable: true },
    {
      key: 'archived_at',
      label: t('pages.archivedColArchivedAt'),
      sortable: true,
      render: (row) => formatDateSimple(row.archived_at),
    },
    {
      key: 'archived_by_name',
      label: t('pages.archivedColArchivedBy'),
      sortable: true,
      render: (row) => row.archived_by_name || '—',
    },
    { key: 'unarchive', label: '', slot: 'unarchive', width: '8rem', align: 'text-right' },
  ]);
</script>

<Modal bind:isOpen maxWidth="max-w-3xl">
  <ModalHeader
    title={t('pages.archivedHeading')}
    onClose={() => (isOpen = false)}
  />
  <div class="archived-body" data-testid="archived-pages-modal">
    <DataTable
      {columns}
      data={rows}
      keyField="id"
      {loading}
      emptyMessage={t('pages.archivedEmpty')}
      emptyIcon={Archive}
    >
      {#snippet unarchive(row)}
        <Button
          variant="secondary"
          size="small"
          disabled={pendingId === row.id}
          onclick={() => handleUnarchive(row)}
          data-testid="archived-page-unarchive"
          data-page-id={row.id}
        >
          {t('pages.archivedUnarchive')}
        </Button>
      {/snippet}
    </DataTable>
  </div>
</Modal>

<style>
  .archived-body {
    padding: 1rem 1.25rem 1.25rem;
  }
</style>
