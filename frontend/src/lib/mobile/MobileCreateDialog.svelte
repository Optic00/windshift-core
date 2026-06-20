<script>
  import { api } from '../api.js';
  import { navigate } from '../router.js';
  import { workspacesStore } from '../stores';
  import Modal from '../dialogs/Modal.svelte';

  /**
   * @typedef {Object} ParentItem
   * @property {number} id
   * @property {string} title
   */

  /**
   * @param {Object} opts
   * @param {boolean} [opts.isOpen]
   * @param {(() => void) | null} [opts.onclose]
   * @param {ParentItem | null} [opts.parent] — when set, the new item is
   *   created as a child of this item and the type picker is locked to the
   *   available sub-issue types for that item's level.
   * @param {Array<{id: number, name?: string}> | null} [opts.availableItemTypes]
   *   — sub-issue types allowed under `parent`; passed in by the caller (the
   *   mobile item detail computes them the same way the desktop store does).
   * @param {number | null} [opts.workspaceId] — the parent item's workspace,
   *   used to lock the workspace picker when creating a child.
   */
  let {
    isOpen = $bindable(false),
    onclose = null,
    parent = null,
    availableItemTypes = null,
    workspaceId: lockedWorkspaceId = null,
  } = $props();

  let title = $state('');
  let description = $state('');
  let workspaceId = $state(null);
  let itemTypeId = $state(null);
  let itemTypes = $state([]);
  let typesLoading = $state(false);
  let saving = $state(false);
  let error = $state('');
  let lastTypeWorkspace = null;

  const isChild = $derived(!!parent);
  const workspaces = $derived($workspacesStore.regularWorkspaces ?? []);
  const canSubmit = $derived(title.trim() !== '' && !!workspaceId && !!itemTypeId && !saving);

  // Default the workspace when the dialog opens (first regular workspace), or
  // lock it to the parent item's workspace when creating a child.
  $effect(() => {
    if (!isOpen) return;
    if (isChild && lockedWorkspaceId) {
      workspaceId = lockedWorkspaceId;
      return;
    }
    if (!workspaceId && workspaces.length > 0) {
      workspaceId = workspaces[0].id;
    }
  });

  // Resolve the item-type list. When creating a child the caller hands us the
  // exact set of allowed sub-issue types (pre-computed from the hierarchy), so
  // there's nothing to fetch — we just adopt them. Otherwise we load the full
  // workspace-scoped list whenever the chosen workspace changes.
  $effect(() => {
    if (!isOpen) return;
    if (isChild) {
      const allowed = Array.isArray(availableItemTypes) ? availableItemTypes : [];
      itemTypes = allowed;
      if (!allowed.some((t) => t.id === itemTypeId)) {
        itemTypeId = allowed[0]?.id ?? null;
      }
      return;
    }
    const wsId = workspaceId;
    if (!wsId || wsId === lastTypeWorkspace) return;
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
      const result = await api.items.create({
        title: title.trim(),
        description: description.trim(),
        workspace_id: workspaceId,
        item_type_id: itemTypeId,
        // Creating a child: pin it to the parent so it shows up under it.
        parent_id: isChild ? parent.id : undefined,
      });
      // When creating a child we stay on the parent's detail view and let the
      // caller refresh the sub-item list rather than navigating away.
      if (isChild) {
        handleClose();
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
    <h2 class="title">{isChild ? 'New sub-item' : 'New item'}</h2>

    {#if isChild}
      <p class="parent" data-testid="create-parent">
        Under <strong>{parent.title}</strong>
      </p>
    {/if}

    <label class="field">
      <span>Title</span>
      <input
        bind:value={title}
        placeholder="What needs doing?"
        data-testid="create-title"
        autocomplete="off"
      />
    </label>

    <div class="row">
      <label class="field">
        <span>Workspace</span>
        <select bind:value={workspaceId} disabled={isChild} data-testid="create-workspace">
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

    {#if error}<p class="error" data-testid="create-error">{error}</p>{/if}

    <div class="actions">
      <button class="btn-cancel" onclick={handleClose} type="button">Cancel</button>
      <button class="btn-create" onclick={submit} disabled={!canSubmit} data-testid="create-submit" type="button">
        {saving ? 'Creating…' : 'Create'}
      </button>
    </div>
  </div>
</Modal>

<style>
  .create { display: flex; flex-direction: column; gap: 0.85rem; padding: 1rem; }
  .title { margin: 0; font-size: 1.0625rem; font-weight: var(--font-semibold, 600); color: var(--ds-text); }

  .parent { margin: -0.25rem 0 0; font-size: 0.8125rem; color: var(--ds-text-subtle); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

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
  .field select:disabled { opacity: 0.7; }

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
