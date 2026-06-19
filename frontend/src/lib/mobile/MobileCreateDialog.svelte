<script>
  import { api } from '../api.js';
  import { navigate } from '../router.js';
  import { workspacesStore } from '../stores';
  import Modal from '../dialogs/Modal.svelte';

  /**
   * @typedef {'work' | 'personal'} CreateMode
   * 'personal' targets the user's personal workspace and submits a
   * title-only task (no item type) — the same shape the desktop
   * PersonalTasksPanel uses to add a personal task. 'work' is the
   * default full work-item form for regular workspaces.
   */

  /** @type {{ isOpen?: boolean, mode?: CreateMode, onclose?: (() => void) | null }} */
  let { isOpen = $bindable(false), mode = 'work', onclose = null } = $props();

  let title = $state('');
  let description = $state('');
  let workspaceId = $state(null);
  let itemTypeId = $state(null);
  let itemTypes = $state([]);
  let typesLoading = $state(false);
  let saving = $state(false);
  let error = $state('');
  let lastTypeWorkspace = null;

  const workspaces = $derived($workspacesStore.regularWorkspaces ?? []);
  // Personal workspace is loaded on-demand; the store keeps it once fetched.
  const personalWorkspace = $derived($workspacesStore.personalWorkspace ?? null);
  const isPersonal = $derived(mode === 'personal');

  const canSubmit = $derived(
    title.trim() !== '' &&
      !saving &&
      // Work mode needs a workspace + item type; personal mode just needs a
      // resolved personal workspace (item type resolves to the default on the
      // server, matching the desktop personal-task creation path).
      (isPersonal ? !!personalWorkspace : !!workspaceId && !!itemTypeId)
  );

  // Default the workspace when the dialog opens (first regular workspace).
  $effect(() => {
    if (isOpen && !workspaceId && workspaces.length > 0) {
      workspaceId = workspaces[0].id;
    }
  });

  // Load the personal workspace on-demand when the dialog opens in personal
  // mode (the mobile shell otherwise never touches it outside the Personal tab).
  $effect(() => {
    if (isOpen && isPersonal && !personalWorkspace) {
      workspacesStore.loadPersonalWorkspace();
    }
  });

  // Load item types whenever the chosen workspace changes (work mode only).
  $effect(() => {
    const wsId = workspaceId;
    if (isPersonal || !isOpen || !wsId || wsId === lastTypeWorkspace) return;
    lastTypeWorkspace = wsId;
    loadTypes(wsId);
  });

  async function loadTypes(wsId) {
    typesLoading = true;
    try {
      const res = await api.itemTypes.getAll({ workspace_id: wsId });
      itemTypes = Array.isArray(res) ? res : (res?.items ?? []);
      // Keep the current type if still valid, else default to the first.
      if (!itemTypes.some((t) => t.id === itemTypeId)) {
        itemTypeId = itemTypes[0]?.id ?? null;
      }
    } catch (err) {
      console.error('Failed to load item types:', err);
      itemTypes = [];
      itemTypeId = null;
    } finally {
      typesLoading = false;
    }
  }

  function reset() {
    title = '';
    description = '';
    itemTypeId = null;
    itemTypes = [];
    error = '';
    lastTypeWorkspace = null;
    // workspaceId is intentionally kept so repeated creates default to the same.
  }

  function handleClose() {
    isOpen = false;
    reset();
    onclose?.();
  }

  async function submit() {
    if (!canSubmit) return;
    saving = true;
    error = '';
    try {
      const result = await api.items.create(
        isPersonal
          ? { title: title.trim(), workspace_id: personalWorkspace.id }
          : {
              title: title.trim(),
              description: description.trim(),
              workspace_id: workspaceId,
              item_type_id: itemTypeId,
            }
      );
      if (isPersonal) {
        // The newly created personal task lives in this tab's list — let the
        // active Personal view refresh itself. BroadcastChannel excludes the
        // posting tab, so the same-tab notice is a window event instead.
        window.dispatchEvent(new CustomEvent('personal-task-created'));
        handleClose();
        // Stay on the Personal checklist so the user can keep adding tasks,
        // matching the desktop PersonalTasksPanel behavior.
      } else {
        handleClose();
        if (result?.id) navigate(`/m/items/${result.id}`);
      }
    } catch (err) {
      console.error('Failed to create item:', err);
      error = err?.message || 'Could not create the item.';
    } finally {
      saving = false;
    }
  }
</script>

<Modal bind:isOpen maxWidth="max-w-md" zIndexClass="z-[600]" onSubmit={submit} submitDisabled={!canSubmit} onclose={handleClose}>
  <div class="create" data-testid="mobile-create-dialog">
    <h2 class="title">{isPersonal ? 'New personal task' : 'New item'}</h2>

    <label class="field">
      <span>{isPersonal ? 'Task' : 'Title'}</span>
      <input
        bind:value={title}
        placeholder={isPersonal ? 'What do you need to do?' : 'What needs doing?'}
        data-testid="create-title"
        autocomplete="off"
      />
    </label>

    {#if !isPersonal}
      <div class="row">
        <label class="field">
          <span>Workspace</span>
          <select bind:value={workspaceId} data-testid="create-workspace">
            {#each workspaces as ws (ws.id)}
              <option value={ws.id}>{ws.name}</option>
            {/each}
          </select>
        </label>

        <label class="field">
          <span>Type</span>
          <select bind:value={itemTypeId} disabled={typesLoading || itemTypes.length === 0} data-testid="create-type">
            {#each itemTypes as it (it.id)}
              <option value={it.id}>{it.name}</option>
            {/each}
          </select>
        </label>
      </div>

      <label class="field">
        <span>Description <em>(optional)</em></span>
        <textarea bind:value={description} rows="3" placeholder="Add detail…" data-testid="create-description"></textarea>
      </label>
    {/if}

    {#if error}<p class="error" data-testid="create-error">{error}</p>{/if}

    <div class="actions">
      <button class="btn-cancel" onclick={handleClose} type="button">Cancel</button>
      <button class="btn-create" onclick={submit} disabled={!canSubmit} data-testid="create-submit" type="button">
        {saving ? 'Creating…' : isPersonal ? 'Add task' : 'Create'}
      </button>
    </div>
  </div>
</Modal>

<style>
  .create { display: flex; flex-direction: column; gap: 0.85rem; padding: 1rem; }
  .title { margin: 0; font-size: 1.0625rem; font-weight: var(--font-semibold, 600); color: var(--ds-text); }

  .row { display: flex; gap: 0.75rem; }
  .row .field { flex: 1; min-width: 0; }

  .field { display: flex; flex-direction: column; gap: 0.3rem; font-size: 0.75rem; color: var(--ds-text-subtle); }
  .field em { font-style: normal; opacity: 0.7; }
  .field input, .field select, .field textarea {
    padding: 0.6rem; border: 1px solid var(--ds-border); border-radius: var(--radius-md, 6px);
    background-color: var(--ds-background-input, var(--ds-surface)); color: var(--ds-text);
    font-size: 1rem; /* >=16px avoids iOS zoom-on-focus */
  }
  .field textarea { resize: vertical; font-family: inherit; }

  .error { margin: 0; font-size: 0.8125rem; color: var(--ds-text-danger, var(--ds-danger)); }

  .actions { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.25rem; }
  .btn-cancel {
    padding: 0.6rem 1rem; border: 1px solid var(--ds-border); border-radius: var(--radius-md, 6px);
    background: var(--ds-surface); color: var(--ds-text); cursor: pointer; min-height: 44px;
  }
  .btn-create {
    padding: 0.6rem 1.5rem; border: none; border-radius: var(--radius-md, 6px);
    background: var(--ds-interactive); color: var(--ds-text-inverse, #fff);
    font-weight: var(--font-semibold, 600); cursor: pointer; min-height: 44px;
  }
  .btn-create:disabled { opacity: 0.6; }
</style>
