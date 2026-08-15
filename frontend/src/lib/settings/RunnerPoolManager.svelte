<script>
  // Admin UI for runner pools (WI-237). A runner_pool is an ActionCapability;
  // here admins mint/revoke its registration tokens and view/revoke the runner
  // instances registered against it. Backend lifecycle: WI-177.
  import { onMount } from 'svelte';
  import { api } from '../api.js';
  import { Plus, Trash2, Copy, Server, KeyRound } from '@lucide/svelte';
  import Button from '../components/Button.svelte';
  import PageHeader from '../layout/PageHeader.svelte';
  import Modal from '../dialogs/Modal.svelte';
  import ModalHeader from '../dialogs/ModalHeader.svelte';
  import Spinner from '../components/Spinner.svelte';
  import Lozenge from '../components/Lozenge.svelte';
  import DataTable from '../components/DataTable.svelte';
  import Checkbox from '../components/Checkbox.svelte';
  import Input from '../components/Input.svelte';
  import { successToast, errorToast } from '../stores/toasts.svelte.js';
  import { confirm } from '../composables/useConfirm.js';
  import { formatAuthenticatedDateTime } from '../utils/authenticatedDateFormatter.js';

  let pools = $state([]);
  let loadingPools = $state(true);
  let selectedPool = $state(null);

  let tokens = $state([]);
  let loadingTokens = $state(false);
  let instances = $state([]);
  let loadingInstances = $state(false);

  // Create-pool modal
  let showCreatePool = $state(false);
  let creating = $state(false);
  let poolForm = $state({ name: '', maxConcurrent: 0, appliesToAll: true, enabled: true });

  // Mint-token modal + one-time reveal. mintedToken carries the plaintext
  // token AND the server-generated copy-paste install command (WI-309) —
  // public WS_API_URL, version-matched images, and the token baked in.
  let showMint = $state(false);
  let minting = $state(false);
  let mintForm = $state({ description: '', ttlHours: 0 });
  let mintedToken = $state(null); // { token, installCommand }

  const canCreate = $derived(poolForm.name.trim().length > 0 && Number(poolForm.maxConcurrent) >= 0);

  onMount(loadPools);

  async function loadPools() {
    loadingPools = true;
    try {
      const all = await api.actionCapabilities.getAll();
      pools = (all || []).filter((c) => c.capability_type === 'runner_pool');
      // Keep the selection valid across reloads.
      if (selectedPool) {
        selectedPool = pools.find((p) => p.id === selectedPool.id) || null;
      }
    } catch (e) {
      errorToast(e?.message || 'Failed to load runner pools');
    } finally {
      loadingPools = false;
    }
  }

  async function selectPool(pool) {
    selectedPool = pool;
    await Promise.all([loadTokens(pool.id), loadInstances(pool.id)]);
  }

  async function loadTokens(poolId) {
    loadingTokens = true;
    try {
      tokens = (await api.runnerPools.listTokens(poolId)) || [];
    } catch (e) {
      errorToast(e?.message || 'Failed to load tokens');
    } finally {
      loadingTokens = false;
    }
  }

  async function loadInstances(poolId) {
    loadingInstances = true;
    try {
      instances = (await api.runnerPools.listInstances(poolId)) || [];
    } catch (e) {
      errorToast(e?.message || 'Failed to load runner instances');
    } finally {
      loadingInstances = false;
    }
  }

  function openCreate() {
    poolForm = { name: '', maxConcurrent: 0, appliesToAll: true, enabled: true };
    showCreatePool = true;
  }

  async function createPool() {
    if (!canCreate) return;
    creating = true;
    try {
      await api.actionCapabilities.create({
        name: poolForm.name.trim(),
        capability_type: 'runner_pool',
        config: JSON.stringify({ max_concurrent_runs: Number(poolForm.maxConcurrent) || 0 }),
        is_enabled: poolForm.enabled,
        applies_to_all_workspaces: poolForm.appliesToAll,
        workspace_ids: [],
      });
      successToast('Runner pool created');
      showCreatePool = false;
      await loadPools();
    } catch (e) {
      errorToast(e?.message || 'Failed to create runner pool');
    } finally {
      creating = false;
    }
  }

  function openMint() {
    mintForm = { description: '', ttlHours: 0 };
    showMint = true;
  }

  async function mintToken() {
    if (!selectedPool) return;
    minting = true;
    try {
      const res = await api.runnerPools.mintToken(selectedPool.id, {
        description: mintForm.description.trim(),
        ttl_hours: Number(mintForm.ttlHours) || 0,
      });
      showMint = false;
      // Plaintext, shown once.
      mintedToken = res?.token ? { token: res.token, installCommand: res.install_command || '' } : null;
      await loadTokens(selectedPool.id);
    } catch (e) {
      errorToast(e?.message || 'Failed to mint token');
    } finally {
      minting = false;
    }
  }

  async function revokeToken(tok) {
    const ok = await confirm({
      title: 'Revoke registration token',
      message: `Revoke token ${tok.token_prefix}…? Runners can no longer register with it.`,
      confirmText: 'Revoke',
      variant: 'danger',
    });
    if (!ok) return;
    try {
      await api.runnerPools.revokeToken(selectedPool.id, tok.id);
      successToast('Token revoked');
      await loadTokens(selectedPool.id);
    } catch (e) {
      errorToast(e?.message || 'Failed to revoke token');
    }
  }

  async function revokeInstance(inst) {
    const ok = await confirm({
      title: 'Revoke runner',
      message: `Evict runner ${inst.name || `#${inst.id}`}? It must re-register to rejoin.`,
      confirmText: 'Revoke',
      variant: 'danger',
    });
    if (!ok) return;
    try {
      await api.runnerPools.revokeInstance(selectedPool.id, inst.id);
      successToast('Runner revoked');
      await loadInstances(selectedPool.id);
    } catch (e) {
      errorToast(e?.message || 'Failed to revoke runner');
    }
  }

  async function copy(text) {
    try {
      await navigator.clipboard.writeText(text);
      successToast('Copied to clipboard');
    } catch {
      errorToast('Copy failed — select and copy manually');
    }
  }

  function fmtDate(d) {
    return d ? formatAuthenticatedDateTime(d) : '—';
  }

  function tokenStatus(tok) {
    if (tok.revoked_at) return { label: 'Revoked', appearance: 'removed' };
    if (tok.expires_at && new Date(tok.expires_at) < new Date()) {
      return { label: 'Expired', appearance: 'default' };
    }
    return { label: 'Active', appearance: 'success' };
  }

  // Mirrors the server's RunnerLivenessWindow (90s, ~3 missed heartbeats):
  // past it the lease reaper treats the runner as dead, so the UI must not
  // keep calling it active.
  const HEARTBEAT_FRESH_MS = 90_000;

  function instanceStatus(inst) {
    if (inst.revoked_at) return { label: 'Revoked', appearance: 'removed' };
    if (inst.status === 'active') {
      const lastSeen = inst.last_heartbeat_at || inst.registered_at;
      if (lastSeen && Date.now() - new Date(lastSeen).getTime() <= HEARTBEAT_FRESH_MS) {
        return { label: 'Online', appearance: 'success' };
      }
      return { label: 'Stale', appearance: 'warning' };
    }
    return { label: inst.status || 'Unknown', appearance: 'default' };
  }

  const poolColumns = [
    { key: 'name', label: 'Pool', slot: 'name' },
    { key: 'status', label: 'Status', slot: 'status' },
    { key: 'concurrency', label: 'Max concurrent', slot: 'concurrency' },
  ];
  const tokenColumns = [
    { key: 'token_prefix', label: 'Token', slot: 'prefix' },
    { key: 'description', label: 'Description', slot: 'description' },
    { key: 'created_at', label: 'Created', slot: 'created' },
    { key: 'expires_at', label: 'Expires', slot: 'expires' },
    { key: 'status', label: 'Status', slot: 'status' },
    { key: 'actions', label: '', slot: 'actions', align: 'text-right', width: 'w-20' },
  ];
  const instanceColumns = [
    { key: 'name', label: 'Runner', slot: 'name' },
    { key: 'status', label: 'Status', slot: 'status' },
    { key: 'registered_at', label: 'Registered', slot: 'registered' },
    { key: 'last_heartbeat_at', label: 'Last heartbeat', slot: 'heartbeat' },
    { key: 'actions', label: '', slot: 'actions', align: 'text-right', width: 'w-20' },
  ];

  function maxConcurrentLabel(pool) {
    try {
      const n = JSON.parse(pool.config || '{}').max_concurrent_runs ?? 0;
      return Number(n) === 0 ? 'Unlimited' : String(n);
    } catch {
      return 'Unlimited';
    }
  }
</script>

<div class="space-y-6">
  <PageHeader title="Runner pools" subtitle="Pools of remote runners that execute coding-agent jobs. Mint a registration token to add a runner host; see RUNNER setup docs.">
    {#snippet actions()}
      <!-- shortcut-guard-exempt: admin settings tab action, not a primary global-create surface -->
      <Button variant="primary" onclick={openCreate} icon={Plus}>
        New pool
      </Button>
    {/snippet}
  </PageHeader>

  {#if loadingPools}
    <div class="flex justify-center py-10"><Spinner /></div>
  {:else}
    <DataTable
      columns={poolColumns}
      data={pools}
      keyField="id"
      onRowClick={selectPool}
      selectedItemId={selectedPool?.id ?? null}
      emptyIcon={Server}
      emptyMessage="No runner pools"
      emptyDescription="Create a pool, then mint a registration token to connect a runner host."
    >
      {#snippet name(pool)}
        <span class="font-medium" style="color: var(--ds-text);">{pool.name}</span>
      {/snippet}
      {#snippet status(pool)}
        <Lozenge appearance={pool.is_enabled ? 'success' : 'default'} size="sm">
          {pool.is_enabled ? 'Enabled' : 'Disabled'}
        </Lozenge>
      {/snippet}
      {#snippet concurrency(pool)}
        <span class="text-sm" style="color: var(--ds-text-subtle);">{maxConcurrentLabel(pool)}</span>
      {/snippet}
    </DataTable>
  {/if}

  {#if selectedPool}
    <!-- Registration tokens -->
    <section class="space-y-3">
      <div class="flex items-center justify-between">
        <h3 class="text-sm font-semibold flex items-center gap-2" style="color: var(--ds-text);">
          <KeyRound size={16} /> Registration tokens — {selectedPool.name}
        </h3>
        <Button variant="secondary" size="sm" onclick={openMint}>
          <Plus size={14} /> Mint token
        </Button>
      </div>
      {#if loadingTokens}
        <div class="flex justify-center py-6"><Spinner /></div>
      {:else}
        <DataTable columns={tokenColumns} data={tokens} keyField="id" emptyMessage="No tokens yet">
          {#snippet prefix(tok)}
            <code class="text-xs" style="color: var(--ds-text);">{tok.token_prefix}…</code>
          {/snippet}
          {#snippet description(tok)}
            <span class="text-sm" style="color: var(--ds-text-subtle);">{tok.description || '—'}</span>
          {/snippet}
          {#snippet created(tok)}
            <span class="text-xs" style="color: var(--ds-text-subtle);">{fmtDate(tok.created_at)}</span>
          {/snippet}
          {#snippet expires(tok)}
            <span class="text-xs" style="color: var(--ds-text-subtle);">{tok.expires_at ? fmtDate(tok.expires_at) : 'Never'}</span>
          {/snippet}
          {#snippet status(tok)}
            {@const s = tokenStatus(tok)}
            <Lozenge appearance={s.appearance} size="sm">{s.label}</Lozenge>
          {/snippet}
          {#snippet actions(tok)}
            {#if !tok.revoked_at}
              <Button variant="danger-ghost" size="small" icon={Trash2} title="Revoke token" onclick={() => revokeToken(tok)}></Button>
            {/if}
          {/snippet}
        </DataTable>
      {/if}
    </section>

    <!-- Runner instances -->
    <section class="space-y-3">
      <h3 class="text-sm font-semibold flex items-center gap-2" style="color: var(--ds-text);">
        <Server size={16} /> Runners — {selectedPool.name}
      </h3>
      {#if loadingInstances}
        <div class="flex justify-center py-6"><Spinner /></div>
      {:else}
        <DataTable columns={instanceColumns} data={instances} keyField="id" emptyMessage="No runners registered">
          {#snippet name(inst)}
            <span class="font-medium text-sm" style="color: var(--ds-text);">{inst.name || `#${inst.id}`}</span>
          {/snippet}
          {#snippet status(inst)}
            {@const s = instanceStatus(inst)}
            <Lozenge appearance={s.appearance} size="sm">{s.label}</Lozenge>
          {/snippet}
          {#snippet registered(inst)}
            <span class="text-xs" style="color: var(--ds-text-subtle);">{fmtDate(inst.registered_at)}</span>
          {/snippet}
          {#snippet heartbeat(inst)}
            <span class="text-xs" style="color: var(--ds-text-subtle);">{fmtDate(inst.last_heartbeat_at)}</span>
          {/snippet}
          {#snippet actions(inst)}
            {#if !inst.revoked_at}
              <Button variant="danger-ghost" size="small" icon={Trash2} title="Revoke runner" onclick={() => revokeInstance(inst)}></Button>
            {/if}
          {/snippet}
        </DataTable>
      {/if}
    </section>
  {/if}
</div>

{#if showCreatePool}
  <Modal isOpen={true} onclose={() => (showCreatePool = false)} onSubmit={createPool} submitDisabled={!canCreate || creating}>
    {#snippet children(submitHint)}
      <ModalHeader title="New runner pool" onclose={() => (showCreatePool = false)} />
      <div class="space-y-4 p-4">
        <label class="block">
          <span class="text-sm font-medium" style="color: var(--ds-text);">Name</span>
          <Input
            class="mt-1"
            size="small"
            bind:value={poolForm.name}
            placeholder="e.g. default-runners"
          />
        </label>
        <label class="block">
          <span class="text-sm font-medium" style="color: var(--ds-text);">Max concurrent runs</span>
          <Input
            type="number"
            min="0"
            class="mt-1"
            size="small"
            bind:value={poolForm.maxConcurrent}
          />
          <span class="text-xs" style="color: var(--ds-text-subtle);">0 = unlimited</span>
        </label>
        <Checkbox bind:checked={poolForm.appliesToAll} label="Available to all workspaces" size="small" />
        <Checkbox bind:checked={poolForm.enabled} label="Enabled" size="small" />
        <div class="flex justify-end gap-2 pt-2 border-t" style="border-color: var(--ds-border);">
          <Button variant="secondary" onclick={() => (showCreatePool = false)} keyboardHint="Esc">Cancel</Button>
          <Button variant="primary" onclick={createPool} loading={creating} disabled={!canCreate} keyboardHint={submitHint}>Create pool</Button>
        </div>
      </div>
    {/snippet}
  </Modal>
{/if}

{#if showMint}
  <Modal isOpen={true} onclose={() => (showMint = false)} onSubmit={mintToken} submitDisabled={minting}>
    {#snippet children(submitHint)}
      <ModalHeader title="Mint registration token" onclose={() => (showMint = false)} />
      <div class="space-y-4 p-4">
        <label class="block">
          <span class="text-sm font-medium" style="color: var(--ds-text);">Description (optional)</span>
          <Input
            class="mt-1"
            size="small"
            bind:value={mintForm.description}
            placeholder="e.g. eu-west runner box"
          />
        </label>
        <label class="block">
          <span class="text-sm font-medium" style="color: var(--ds-text);">Expires in (hours)</span>
          <Input
            type="number"
            min="0"
            class="mt-1"
            size="small"
            bind:value={mintForm.ttlHours}
          />
          <span class="text-xs" style="color: var(--ds-text-subtle);">0 = never expires (revoke to disable)</span>
        </label>
        <div class="flex justify-end gap-2 pt-2 border-t" style="border-color: var(--ds-border);">
          <Button variant="secondary" onclick={() => (showMint = false)} keyboardHint="Esc">Cancel</Button>
          <Button variant="primary" onclick={mintToken} loading={minting} keyboardHint={submitHint}>Mint token</Button>
        </div>
      </div>
    {/snippet}
  </Modal>
{/if}

{#if mintedToken}
  <Modal isOpen={true} onclose={() => (mintedToken = null)}>
    {#snippet children()}
      <ModalHeader title="Add the runner host" onclose={() => (mintedToken = null)} />
      <div class="space-y-4 p-4">
        {#if mintedToken.installCommand}
          <div class="space-y-2">
            <p class="text-sm" style="color: var(--ds-text-subtle);">
              Run this on the runner host. It installs and starts the runner container with
              this server's URL, the matching image tag, and the fresh token already baked in:
            </p>
            <div class="flex items-start gap-2">
              <code
                data-testid="runner-install-command"
                class="flex-1 overflow-x-auto whitespace-pre rounded-md border px-3 py-2 text-xs"
                style="background: var(--ds-surface-sunken); color: var(--ds-text); border-color: var(--ds-border);"
              >{mintedToken.installCommand}</code>
              <Button variant="primary" size="sm" dataTestid="copy-install-command" onclick={() => copy(mintedToken.installCommand)}>
                <Copy size={14} /> Copy
              </Button>
            </div>
          </div>
        {/if}
        <div class="space-y-2">
          <p class="text-sm" style="color: var(--ds-text-subtle);">
            {#if mintedToken.installCommand}Setting up manually instead? Set the token as{:else}Set the token as{/if}
            <code>WSRUNNER_REGISTRATION_TOKEN</code> on the runner host. It is shown
            <strong>once</strong> and cannot be retrieved again.
          </p>
          <div class="flex items-center gap-2">
            <code
              data-testid="runner-registration-token"
              class="flex-1 overflow-x-auto rounded-md border px-3 py-2 text-xs"
              style="background: var(--ds-surface-sunken); color: var(--ds-text); border-color: var(--ds-border);"
            >{mintedToken.token}</code>
            <Button variant="secondary" size="sm" dataTestid="copy-registration-token" onclick={() => copy(mintedToken.token)}>
              <Copy size={14} /> Copy
            </Button>
          </div>
        </div>
        <div class="flex justify-end pt-2 border-t" style="border-color: var(--ds-border);">
          <Button variant="primary" dataTestid="runner-token-done" onclick={() => (mintedToken = null)}>Done</Button>
        </div>
      </div>
    {/snippet}
  </Modal>
{/if}
