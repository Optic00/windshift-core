<script>
  import Modal from '../../dialogs/Modal.svelte';
  import ModalHeader from '../../dialogs/ModalHeader.svelte';
  import DialogFooter from '../../dialogs/DialogFooter.svelte';
  import Button from '../../components/Button.svelte';
  import Select from '../../components/Select.svelte';
  import UserPicker from '../../pickers/UserPicker.svelte';
  import GroupPicker from '../../pickers/GroupPicker.svelte';
  import RolePicker from '../../pickers/RolePicker.svelte';
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
        {#if data.acl.length === 0}
          <p class="status empty">No explicit grants on this page.</p>
        {:else}
          <table class="acl-table">
            <thead>
              <tr>
                <th>Principal</th>
                <th>Level</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {#each data.acl as row (row.id)}
                <tr data-testid="page-acl-row">
                  <td>{row.principal_type} #{row.principal_id}</td>
                  <td>{row.permission_level}</td>
                  <td class="row-actions">
                    {#if isAdmin}
                      <button
                        type="button"
                        class="link-button"
                        onclick={() => revoke(row.id)}
                        disabled={saving}
                      >
                        Remove
                      </button>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}

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

  .acl-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.875rem;
  }

  .acl-table th,
  .acl-table td {
    text-align: left;
    padding: 0.375rem 0.5rem;
    border-bottom: 1px solid var(--ds-border, #e5e7eb);
  }

  .row-actions {
    text-align: right;
  }

  .link-button {
    background: transparent;
    border: none;
    color: var(--ds-text-danger, #b91c1c);
    cursor: pointer;
    font-size: 0.875rem;
    padding: 0;
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

  .status.empty {
    text-align: center;
    padding: 0.75rem;
  }
</style>
