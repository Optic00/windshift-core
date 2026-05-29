<script>
  // Coding-agent harness — global allowlist editor (WI-87).
  //
  // Loads agentSecurity.listAllowlist() + the user and workspace
  // catalogs once on mount so we can render names instead of bare ids,
  // then exposes add + remove. Remove goes through the shared
  // ConfirmWithReasonDialog because the backend DELETE requires a
  // non-empty reason for audit-logging.

  import { onMount } from 'svelte';
  import { Loader2, Plus, Trash2 } from '@lucide/svelte';
  import { agentSecurity, api } from '../api.js';
  import UserPicker from '../pickers/UserPicker.svelte';
  import WorkspacePicker from '../pickers/WorkspacePicker.svelte';
  import Input from '../components/Input.svelte';
  import ConfirmWithReasonDialog from '../dialogs/ConfirmWithReasonDialog.svelte';
  import DescriptionText from '../components/DescriptionText.svelte';
  import { errorToast } from '../stores/toasts.svelte.js';

  // flagEnabled is informational: when false, we hint that grants are
  // staged but inert. Loading + state are otherwise self-contained.
  let { flagEnabled = false } = $props();

  let entries = $state([]);
  let loading = $state(true);
  let usersById = $state({});
  // Subset of usersById restricted to centralized service users (is_agent
  // true + no owner). The backend rejects anything else from the
  // allowlist; the UserPicker only ever offers these so admins can't
  // even pick something the server is going to refuse.
  let serviceUsers = $state([]);
  let workspacesById = $state({});

  // Add-form state. addWorkspaceIds is empty when the admin hasn't
  // picked any specific workspace — interpreted as a single "any
  // workspace" grant. Each picked id becomes its own grant on submit.
  let addUserId = $state(null);
  let addWorkspaceIds = $state([]);
  let addReason = $state('');
  let adding = $state(false);

  // Remove-dialog state.
  let removeDialogOpen = $state(false);
  let pendingRemove = $state(null); // { userId, workspaceId? }
  let pendingRemoveLabel = $state('');

  onMount(load);

  async function load() {
    loading = true;
    try {
      const [list, users, workspaces] = await Promise.all([
        agentSecurity.listAllowlist(),
        api.getUsers(),
        api.workspaces.getAll(),
      ]);
      entries = list ?? [];
      const um = {};
      const eligible = [];
      for (const u of users ?? []) {
        um[u.id] = u;
        // Centralized service users only: is_agent=true and no owner.
        // Owned agents (agent_owner_user_id set) reach bindings through
        // the WI-87 chokepoint directly; humans must never be impersonated.
        if (u.is_agent && !u.agent_owner_user_id) eligible.push(u);
      }
      usersById = um;
      serviceUsers = eligible;
      const wm = {};
      for (const w of workspaces ?? []) {
        wm[w.id] = w;
      }
      workspacesById = wm;
    } catch (err) {
      console.error('Failed to load agent-security allowlist:', err);
      errorToast(err?.message || 'Failed to load allowlist');
    } finally {
      loading = false;
    }
  }

  function displayUser(userId) {
    const u = usersById[userId];
    if (!u) return `User #${userId}`;
    const full = `${u.first_name ?? ''} ${u.last_name ?? ''}`.trim();
    return full || u.username || u.email || `User #${userId}`;
  }

  function displayWorkspace(workspaceId) {
    if (!workspaceId) return 'Any workspace';
    const w = workspacesById[workspaceId];
    if (!w) return `Workspace #${workspaceId}`;
    return w.name || w.key || `Workspace #${workspaceId}`;
  }

  let canAdd = $derived(!!addUserId && addReason.trim().length > 0 && !adding);

  async function addEntry() {
    if (!canAdd) return;
    const reason = addReason.trim();
    const targets =
      addWorkspaceIds.length === 0
        ? [null] // single "any workspace" grant
        : addWorkspaceIds;
    adding = true;
    try {
      // Sequential because the backend doesn't yet take a batch shape;
      // a mid-loop failure leaves earlier grants in place but reload
      // below makes the resulting state visible either way.
      for (const wsId of targets) {
        const body = { user_id: addUserId, reason };
        if (wsId) body.workspace_id = wsId;
        await agentSecurity.addAllowlist(body);
      }
      addUserId = null;
      addWorkspaceIds = [];
      addReason = '';
      await load();
    } catch (err) {
      errorToast(err?.message || 'Failed to add allowlist entry');
      console.error('Failed to add allowlist entry:', err);
      await load();
    } finally {
      adding = false;
    }
  }

  function openRemoveDialog(entry) {
    pendingRemove = { userId: entry.user_id, workspaceId: entry.workspace_id ?? null };
    pendingRemoveLabel = `${displayUser(entry.user_id)} (${displayWorkspace(entry.workspace_id)})`;
    removeDialogOpen = true;
  }

  async function confirmRemove(reason) {
    if (!pendingRemove) return;
    try {
      await agentSecurity.removeAllowlist(pendingRemove.userId, {
        workspaceId: pendingRemove.workspaceId ?? undefined,
        reason,
      });
      pendingRemove = null;
      pendingRemoveLabel = '';
      await load();
    } catch (err) {
      errorToast(err?.message || 'Failed to remove allowlist entry');
      console.error('Failed to remove allowlist entry:', err);
    }
  }

  function cancelRemove() {
    pendingRemove = null;
    pendingRemoveLabel = '';
  }
</script>

<div>
  <h4 class="text-sm font-medium mb-3" style="color: var(--ds-text);">
    Allowed centralized service users
  </h4>

  {#if !flagEnabled}
    <div class="mb-3">
      <DescriptionText>
        Grants here are persisted but inert until the toggle above is on — populate them ahead of enabling so workspace admins can start binding immediately.
      </DescriptionText>
    </div>
  {/if}

  {#if loading}
    <div class="flex items-center justify-center py-4">
      <Loader2 class="w-4 h-4 animate-spin" style="color: var(--ds-icon-subtle);" />
    </div>
  {:else}
    {#if entries.length === 0}
      <p class="text-sm py-2" style="color: var(--ds-text-subtle);">
        No grants yet. Add a user below to allow workspace admins to bind to them.
      </p>
    {:else}
      <div class="border rounded-md overflow-hidden" style="border-color: var(--ds-border);">
        <table class="w-full text-sm">
          <thead>
            <tr style="background-color: var(--ds-background-neutral);">
              <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">User</th>
              <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Scope</th>
              <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Reason</th>
              <th class="px-3 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {#each entries as entry (`${entry.user_id}:${entry.workspace_id ?? 0}`)}
              <tr class="border-t" style="border-color: var(--ds-border);">
                <td class="px-3 py-2" style="color: var(--ds-text);">{displayUser(entry.user_id)}</td>
                <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{displayWorkspace(entry.workspace_id)}</td>
                <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{entry.reason || '—'}</td>
                <td class="px-3 py-2 text-right">
                  <button
                    type="button"
                    onclick={() => openRemoveDialog(entry)}
                    class="inline-flex items-center justify-center p-1 rounded hover:opacity-80"
                    style="color: var(--ds-icon-danger);"
                    title="Remove grant"
                    aria-label="Remove grant for {displayUser(entry.user_id)}"
                  >
                    <Trash2 class="w-4 h-4" />
                  </button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}

    <div class="mt-4 p-3 rounded-md" style="background-color: var(--ds-background-neutral);">
      <p class="text-xs font-medium mb-2" style="color: var(--ds-text);">Add grant</p>
      <div class="grid grid-cols-1 md:grid-cols-12 gap-3 items-end">
        <div class="md:col-span-4">
          <label for="add-user" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">User</label>
          <div id="add-user">
            <UserPicker bind:value={addUserId} users={serviceUsers} placeholder="Pick a service user" />
          </div>
        </div>
        <div class="md:col-span-3">
          <label for="add-workspace" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">Workspace</label>
          <div id="add-workspace">
            <WorkspacePicker
              bind:value={addWorkspaceIds}
              allowClear
              placeholder="Any workspace"
            />
          </div>
        </div>
        <div class="md:col-span-4">
          <label for="add-reason" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">Reason (audit-logged)</label>
          <Input id="add-reason" bind:value={addReason} placeholder="e.g. pilot rollout for acme-agent" />
        </div>
        <div class="md:col-span-1">
          <button
            type="button"
            onclick={addEntry}
            disabled={!canAdd}
            class="w-full inline-flex items-center justify-center gap-1 px-3 py-2 rounded text-sm font-medium disabled:opacity-50"
            style="background-color: var(--ds-interactive); color: var(--ds-text-inverse);"
          >
            {#if adding}
              <Loader2 class="w-4 h-4 animate-spin" />
            {:else}
              <Plus class="w-4 h-4" />
            {/if}
            Add
          </button>
        </div>
      </div>
    </div>
  {/if}
</div>

<ConfirmWithReasonDialog
  bind:show={removeDialogOpen}
  variant="danger"
  title="Remove allowlist grant?"
  message={`This removes the grant for ${pendingRemoveLabel}. Workspace admins will no longer be able to bind to this acting identity in that scope.`}
  reasonLabel="Reason (audit-logged)"
  reasonPlaceholder="Why are you removing this grant?"
  confirmText="Remove grant"
  onconfirm={confirmRemove}
  oncancel={cancelRemove}
/>
