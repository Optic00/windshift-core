<script>
  import Modal from '../../dialogs/Modal.svelte';
  import ModalHeader from '../../dialogs/ModalHeader.svelte';
  import DialogFooter from '../../dialogs/DialogFooter.svelte';
  import Button from '../../components/Button.svelte';
  import Select from '../../components/Select.svelte';
  import DataTable from '../../components/DataTable.svelte';
  import EmptyState from '../../components/EmptyState.svelte';
  import UserPicker from '../../pickers/UserPicker.svelte';
  import GroupPicker from '../../pickers/GroupPicker.svelte';
  import RolePicker from '../../pickers/RolePicker.svelte';
  import { IconShield as Shield } from '@tabler/icons-svelte-runes';
  import { api } from '../../api.js';
  import { confirm } from '../../composables/useConfirm.js';

  /**
   * Page permissions dialog. Shows the inherit_permissions flag, the
   * effective level for the current user, and the page's own ACL rows.
   * Admins (effective_level === 'admin') can flip inheritance and add or
   * remove ACL rows; lower-tier viewers see the data read-only.
   */
  let {
    isOpen = $bindable(false),
    workspaceId,
    pageId,
    onUpdated = null,
  } = $props();

  let data = $state(null);
  let loading = $state(false);
  let error = $state('');
  let saving = $state(false);

  let newPrincipalType = $state('user');
  /** @type {number | null} principal id bound to whichever picker is active */
  let newPrincipalId = $state(null);
  let newLevel = $state('view');

  const principalTypeOptions = [
    { value: 'user', label: 'User' },
    { value: 'group', label: 'Group' },
    { value: 'role', label: 'Role' },
  ];

  const levelOptions = [
    { value: 'view', label: 'View' },
    { value: 'edit', label: 'Edit' },
    { value: 'admin', label: 'Admin' },
  ];

  const aclColumns = [
    { key: 'principal', label: 'Principal', slot: 'principal' },
    { key: 'permission_level', label: 'Level', slot: 'level' },
    { key: 'remove', label: '', slot: 'remove', width: '6rem', align: 'text-right' },
  ];

  // Reset the principal selection when the type changes — a user id makes
  // no sense once the user has switched to picking a group or role.
  function onPrincipalTypeChange() {
    newPrincipalId = null;
  }

  $effect(() => {
    if (isOpen && workspaceId && pageId) {
      load();
    }
    if (!isOpen) {
      data = null;
      error = '';
      newPrincipalId = null;
      newPrincipalType = 'user';
      newLevel = 'view';
    }
  });

  async function load() {
    loading = true;
    error = '';
    try {
      data = await api.pages.getPermissions(workspaceId, pageId);
    } catch (err) {
      error = err?.message || 'Failed to load permissions';
    } finally {
      loading = false;
    }
  }

  const isAdmin = $derived(data?.effective_level === 'admin');

  async function toggleInheritance() {
    if (!isAdmin) return;
    saving = true;
    error = '';
    try {
      await api.pages.setInheritance(workspaceId, pageId, !data.inherit_permissions);
      await load();
      onUpdated?.();
    } catch (err) {
      error = err?.message || 'Failed to update inheritance';
    } finally {
      saving = false;
    }
  }

  async function addGrant() {
    if (!isAdmin) return;
    if (typeof newPrincipalId !== 'number' || newPrincipalId <= 0) {
      error = 'Pick a principal before adding the grant';
      return;
    }
    saving = true;
    error = '';
    try {
      await api.pages.grantPermission(workspaceId, pageId, {
        principalType: newPrincipalType,
        principalId: newPrincipalId,
        permissionLevel: newLevel,
      });
      newPrincipalId = null;
      await load();
      onUpdated?.();
    } catch (err) {
      error = err?.message || 'Failed to add permission';
    } finally {
      saving = false;
    }
  }

  async function revoke(permissionId) {
    if (!isAdmin) return;
    const ok = await confirm({
      title: 'Remove permission?',
      message: 'This grant will be removed from the page. You can re-add it later.',
      confirmText: 'Remove',
      cancelText: 'Cancel',
      variant: 'danger',
    });
    if (!ok) return;
    saving = true;
    error = '';
    try {
      await api.pages.revokePermission(workspaceId, pageId, permissionId);
      await load();
      onUpdated?.();
    } catch (err) {
      error = err?.message || 'Failed to revoke';
    } finally {
      saving = false;
    }
  }
</script>

<Modal bind:isOpen maxWidth="max-w-2xl">
  <ModalHeader
    title="Page permissions"
    subtitle={data ? `Your effective access: ${data.effective_level || 'none'}` : ''}
    onClose={() => (isOpen = false)}
  />
  <div class="dialog">
    {#if error}
      <div class="error" role="alert">{error}</div>
    {/if}

    {#if loading || !data}
      <p class="status">Loading…</p>
    {:else}
      <section class="inheritance">
        <label class="inheritance-toggle">
          <input
            id="page-perms-inherit-toggle"
            type="checkbox"
            checked={data.inherit_permissions}
            disabled={!isAdmin || saving}
            onchange={toggleInheritance}
          />
          <span>Inherit permissions from ancestors</span>
        </label>
        <p class="hint">
          When inheritance is on and no explicit grants exist, workspace role permissions decide. Breaking inheritance with no grants restricts the page to admins.
        </p>
      </section>

      <section class="acl">
        <h3>Explicit grants</h3>
        <DataTable
          columns={aclColumns}
          data={data.acl}
          keyField="id"
          emptyMessage="No explicit grants on this page."
          emptyDescription="Inheritance and workspace roles still apply."
          emptyIcon={Shield}
          rowAttrs={() => ({ 'data-testid': 'page-acl-row' })}
        >
          {#snippet principal(row)}
            <span style="color: var(--ds-text);">{row.principal_type} #{row.principal_id}</span>
          {/snippet}
          {#snippet level(row)}
            <span style="color: var(--ds-text);">{row.permission_level}</span>
          {/snippet}
          {#snippet remove(row)}
            {#if isAdmin}
              <Button
                variant="link"
                size="small"
                onclick={() => revoke(row.id)}
                disabled={saving}
              >
                Remove
              </Button>
            {/if}
          {/snippet}
        </DataTable>

        {#if isAdmin}
          <form
            class="add-grant"
            onsubmit={(e) => {
              e.preventDefault();
              addGrant();
            }}
          >
            <Select
              id="page-perms-new-principal-type"
              bind:value={newPrincipalType}
              options={principalTypeOptions}
              disabled={saving}
              onchange={onPrincipalTypeChange}
            />
            <div class="principal-picker">
              {#if newPrincipalType === 'user'}
                <UserPicker
                  bind:value={newPrincipalId}
                  {workspaceId}
                  placeholder="Pick a user"
                  disabled={saving}
                />
              {:else if newPrincipalType === 'group'}
                <GroupPicker
                  bind:value={newPrincipalId}
                  placeholder="Pick a group"
                  disabled={saving}
                />
              {:else}
                <RolePicker
                  bind:value={newPrincipalId}
                  placeholder="Pick a role"
                  disabled={saving}
                />
              {/if}
            </div>
            <Select
              id="page-perms-new-level"
              bind:value={newLevel}
              options={levelOptions}
              disabled={saving}
            />
            <Button
              id="page-perms-add-grant"
              type="submit"
              variant="primary"
              size="small"
              disabled={saving || typeof newPrincipalId !== 'number' || newPrincipalId <= 0}
            >
              Add
            </Button>
          </form>
        {/if}
      </section>
    {/if}
  </div>
  <DialogFooter
    cancelLabel="Close"
    cancelTestid="page-perms-close"
    onCancel={() => (isOpen = false)}
  />
</Modal>

<style>
  .dialog {
    padding: 1.25rem 1.5rem;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .inheritance {
    border-top: 1px solid var(--ds-border, #e5e7eb);
    padding-top: 0.75rem;
  }

  .inheritance-toggle {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.9375rem;
    cursor: pointer;
  }

  .acl {
    border-top: 1px solid var(--ds-border, #e5e7eb);
    padding-top: 0.75rem;
  }

  .acl h3 {
    margin: 0 0 0.5rem 0;
    font-size: 0.875rem;
    font-weight: 600;
    text-transform: uppercase;
    color: var(--ds-text-subtle, #6b7280);
  }

  .add-grant {
    display: grid;
    grid-template-columns: minmax(8rem, 1fr) minmax(12rem, 1.5fr) minmax(7rem, 1fr) auto;
    gap: 0.5rem;
    margin-top: 0.75rem;
    align-items: start;
  }

  .principal-picker {
    min-width: 0;
  }

  .error {
    padding: 0.625rem 0.875rem;
    background: var(--ds-status-danger-bg, #fef2f2);
    color: var(--ds-text-danger, #b91c1c);
    border-radius: 0.25rem;
    font-size: 0.875rem;
  }

  .status {
    color: var(--ds-text-subtle, #6b7280);
    font-size: 0.875rem;
  }
</style>
