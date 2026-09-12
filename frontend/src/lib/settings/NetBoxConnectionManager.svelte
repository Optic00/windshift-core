<script>
  import { onMount } from 'svelte';
  import { Plus, Pencil, Trash2, PlugZap, Loader2 } from '@lucide/svelte';
  import { api } from '../api.js';
  import { netboxConnections } from '../api/netbox.js';
  import Button from '../components/Button.svelte';
  import Input from '../components/Input.svelte';
  import Checkbox from '../components/Checkbox.svelte';
  import NativeSelect from '../components/NativeSelect.svelte';
  import FormField from '../components/FormField.svelte';
  import AlertBox from '../components/AlertBox.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Modal from '../dialogs/Modal.svelte';
  import ModalHeader from '../dialogs/ModalHeader.svelte';
  import SectionHeader from '../layout/SectionHeader.svelte';
  import { t } from '../stores/i18n.svelte.js';
  import { successToast, errorToast, warningToast } from '../stores/toasts.svelte.js';
  import { confirm } from '../composables/useConfirm.js';

  let connections = $state([]);
  let workspaces = $state([]);
  let loading = $state(true);
  let saving = $state(false);
  let testingId = $state(null);
  let conflict = $state(false);
  let reloadingConflict = $state(false);
  let showModal = $state(false);
  let editing = $state(null);
  let error = $state('');
  let controller;
  let modalVersion = 0;
  let form = $state(emptyForm());
  let formElement = $state(null);
  let canSave = $derived(
    Boolean(
      form.name.trim() &&
        form.slug &&
        form.base_url.startsWith('https://') &&
        (editing || form.api_token) &&
        (form.applies_to_all_workspaces || form.workspace_ids.length) &&
        !conflict
    )
  );

  function emptyForm() {
    return {
      slug: '',
      name: '',
      enabled: true,
      base_url: '',
      auth_scheme: 'bearer',
      api_token: '',
      applies_to_all_workspaces: false,
      workspace_ids: [],
    };
  }

  onMount(() => {
    void load();
    return () => controller?.abort();
  });

  async function load() {
    controller?.abort();
    const requestController = new AbortController();
    controller = requestController;
    loading = true;
    error = '';
    try {
      const [nextConnections, nextWorkspaces] = await Promise.all([
        netboxConnections.getAll({ signal: requestController.signal }),
        api.workspaces.getAll({}, { signal: requestController.signal }),
      ]);
      if (requestController.signal.aborted || controller !== requestController) return;
      connections = nextConnections;
      workspaces = nextWorkspaces;
    } catch (err) {
      if (
        !requestController.signal.aborted &&
        controller === requestController &&
        err?.name !== 'AbortError'
      ) {
        error = t('netbox.loadFailed');
      }
    } finally {
      if (!requestController.signal.aborted && controller === requestController) loading = false;
    }
  }

  function openCreate() {
    modalVersion++;
    editing = null;
    conflict = false;
    reloadingConflict = false;
    form = emptyForm();
    showModal = true;
  }

  function openEdit(c) {
    modalVersion++;
    editing = c;
    conflict = false;
    reloadingConflict = false;
    setFormFromConnection(c);
    showModal = true;
  }

  function setFormFromConnection(c) {
    form = {
      slug: c.slug,
      name: c.name,
      enabled: c.enabled,
      base_url: c.base_url,
      auth_scheme: c.auth_scheme,
      api_token: '',
      applies_to_all_workspaces: c.applies_to_all_workspaces,
      workspace_ids: [...(c.workspace_ids || [])],
    };
  }

  function toggleWorkspace(id, checked) {
    form.workspace_ids = checked
      ? [...new Set([...form.workspace_ids, id])]
      : form.workspace_ids.filter((x) => x !== id);
  }

  async function save() {
    if (saving || !canSave) {
      formElement?.reportValidity();
      return;
    }
    if (formElement && !formElement.reportValidity()) return;
    saving = true;
    try {
      if (editing) {
        const payload = {
          revision: editing.revision,
          name: form.name,
          enabled: form.enabled,
          applies_to_all_workspaces: form.applies_to_all_workspaces,
          workspace_ids: form.workspace_ids,
        };
        if (form.api_token) payload.api_token = form.api_token;
        await netboxConnections.update(editing.id, payload);
        successToast(t('netbox.updated'));
      } else {
        await netboxConnections.create({
          ...form,
          applies_to_all_workspaces: Boolean(form.applies_to_all_workspaces),
        });
        successToast(t('netbox.created'));
      }
      showModal = false;
      await load();
    } catch (err) {
      if (err?.status === 409 && editing) {
        conflict = true;
      } else {
        errorToast(err?.message || t('netbox.saveFailed'));
      }
    } finally {
      saving = false;
    }
  }

  function closeModal() {
    if (!saving) {
      modalVersion++;
      reloadingConflict = false;
      showModal = false;
    }
  }

  function handleModalClosed() {
    modalVersion++;
    reloadingConflict = false;
  }

  async function reloadConflict() {
    if (!editing || reloadingConflict) return;
    const version = modalVersion;
    const connectionId = editing.id;
    const isCurrentReload = () =>
      version === modalVersion && showModal && editing?.id === connectionId;
    reloadingConflict = true;
    try {
      const fresh = await netboxConnections.get(connectionId);
      if (!isCurrentReload()) return;
      editing = fresh;
      connections = connections.map((connection) =>
        connection.id === connectionId ? fresh : connection
      );
      setFormFromConnection(fresh);
      conflict = false;
    } catch (err) {
      if (isCurrentReload()) errorToast(err?.message || t('netbox.loadFailed'));
    } finally {
      if (isCurrentReload()) reloadingConflict = false;
    }
  }

  async function testConnection(c) {
    if (testingId || !c.enabled) return;
    if (!c.applies_to_all_workspaces && !(c.workspace_ids || []).length) {
      warningToast(t('netbox.noAllowedWorkspace'));
      return;
    }
    testingId = c.id;
    try {
      const result = await netboxConnections.test(c.id);
      successToast(t('netbox.testSucceeded'));
      for (const warning of result?.warnings || []) warningToast(warning);
    } catch {
      errorToast(t('netbox.testFailed'));
    } finally {
      testingId = null;
    }
  }

  async function remove(c) {
    const accepted = await confirm({
      title: t('netbox.delete'),
      message: t('netbox.deleteConfirm', { name: c.name }),
      confirmText: t('common.delete'),
      cancelText: t('common.cancel'),
      variant: 'danger',
    });
    if (!accepted) return;
    try {
      await netboxConnections.delete(c.id);
      successToast(t('netbox.deleted'));
      await load();
    } catch {
      errorToast(t('netbox.deleteFailed'));
    }
  }
</script>

<SectionHeader title={t('netbox.connections')} subtitle={t('netbox.connectionsDescription')}>
  {#snippet actions()}
    <Button size="small" variant="primary" icon={Plus} onclick={openCreate}>
      {t('netbox.addConnection')}
    </Button>
  {/snippet}
</SectionHeader>
<AlertBox variant="info" message={t('netbox.sharingNotice')} class="mb-4" />
{#if error}
  <AlertBox message={error} class="mb-4" />
{/if}
{#if loading}
  <div class="flex justify-center p-8"><Loader2 class="w-5 h-5 animate-spin" /></div>
{:else if !connections.length}
  <EmptyState title={t('netbox.noConnections')} />
{:else}
  <div class="space-y-2">
    {#each connections as c (c.id)}
      <div
        class="flex flex-wrap items-center justify-between gap-3 rounded border p-3"
        style="border-color:var(--ds-border);background:var(--ds-surface-raised)"
      >
        <div class="min-w-0">
          <div class="font-medium truncate" style="color:var(--ds-text)">{c.name}</div>
          <div class="text-xs truncate" style="color:var(--ds-text-subtle)">
            {c.base_url} · {c.enabled ? t('common.enabled') : t('common.disabled')}
          </div>
          {#if !c.enabled}
            <div class="text-xs" style="color:var(--ds-text-subtle)">
              {t('netbox.enableBeforeTesting')}
            </div>
          {/if}
        </div>
        <div class="flex gap-1">
          <Button
            size="small"
            variant="ghost"
            icon={PlugZap}
            loading={testingId === c.id}
            disabled={Boolean(testingId) || !c.enabled}
            title={!c.enabled ? t('netbox.enableBeforeTesting') : null}
            onclick={() => testConnection(c)}
          >{t('netbox.test')}</Button>
          <Button size="small" variant="ghost" icon={Pencil} onclick={() => openEdit(c)}>
            {t('common.edit')}
          </Button>
          <Button size="small" variant="danger-ghost" icon={Trash2} onclick={() => remove(c)}>
            {t('common.delete')}
          </Button>
        </div>
      </div>
    {/each}
  </div>
{/if}

<Modal
  bind:isOpen={showModal}
  preventClose={saving}
  closeOnBackdropClick={false}
  onSubmit={save}
  submitDisabled={saving || !canSave}
  onclose={handleModalClosed}
>
  <ModalHeader
    title={editing ? t('netbox.editConnection') : t('netbox.addConnection')}
    onclose={closeModal}
  />
  <form
    bind:this={formElement}
    class="p-6"
    onsubmit={(e) => {
      e.preventDefault();
      save();
    }}
  >
    <div class="grid grid-cols-1 md:grid-cols-2 gap-x-4">
      <FormField id="netbox-name" label={t('netbox.name')} required>
        <Input id="netbox-name" bind:value={form.name} maxlength={120} required />
      </FormField>
      <FormField id="netbox-slug" label={t('netbox.slug')} required>
        <Input id="netbox-slug" bind:value={form.slug} readonly={!!editing} required />
      </FormField>
      <FormField id="netbox-base-url" label={t('netbox.baseUrl')} required helper={editing ? t('netbox.immutableHint') : ''}>
        <Input id="netbox-base-url" type="url" bind:value={form.base_url} readonly={!!editing} pattern="https://.*" required />
      </FormField>
      <FormField id="netbox-auth-scheme" label={t('netbox.authScheme')} required>
        <NativeSelect id="netbox-auth-scheme" bind:value={form.auth_scheme} disabled={!!editing} options={[{ value: 'bearer', label: t('netbox.bearer') }, { value: 'token', label: t('netbox.legacy') }]} />
      </FormField>
    </div>
    <FormField id="netbox-api-token" label={t('netbox.apiToken')} required={!editing} helper={editing ? t('netbox.tokenPreserve') : ''}>
      <Input id="netbox-api-token" type="password" bind:value={form.api_token} autocomplete="new-password" required={!editing} />
    </FormField>
    <AlertBox variant="info" message={t('netbox.sharingNotice')} class="mb-4" />
    {#if conflict}
      <AlertBox variant="warning" message={t('netbox.editConflict')} class="mb-4" />
      <Button
        variant="secondary"
        loading={reloadingConflict}
        onclick={reloadConflict}
        class="mb-4"
      >
        {t('netbox.discardAndReload')}
      </Button>
    {/if}
    <Checkbox bind:checked={form.applies_to_all_workspaces} label={t('netbox.allWorkspaces')} />
    {#if !form.applies_to_all_workspaces}
      <FormField label={t('netbox.allowedWorkspaces')} required error={!form.workspace_ids.length ? t('netbox.selectWorkspace') : ''}>
        <div class="mt-2 max-h-40 overflow-y-auto rounded border p-3 flex flex-col items-start gap-2" style="border-color:var(--ds-border)">
          {#each workspaces as w}
            <Checkbox checked={form.workspace_ids.includes(w.id)} onchange={(checked) => toggleWorkspace(w.id, checked)} label={`${w.key} - ${w.name}`} />
          {/each}
        </div>
      </FormField>
    {/if}
    <Checkbox bind:checked={form.enabled} label={t('netbox.enabled')} />
    <div class="flex justify-end gap-2 pt-5">
      <Button variant="ghost" disabled={saving} onclick={closeModal}>{t('common.cancel')}</Button>
      <Button type="submit" variant="primary" loading={saving} disabled={saving || !canSave}>
        {t('netbox.save')}
      </Button>
    </div>
  </form>
</Modal>
