<script>
  // Workspace agent bindings (WI-88). The page workspace admins use to
  // wire up coding-agent runners: pick an acting identity (an
  // allowlisted centralized service user), point it at a repo via
  // one of the workspace's SCM connections, set token budget knobs.
  // Backend chokepoint re-validates the identity at create time; the
  // candidates endpoint just keeps the picker honest.

  import { onMount } from 'svelte';
  import { Bot, FlaskConical, Loader2, Plus, Trash2 } from '@lucide/svelte';
  import { agentBindings, api } from '../api.js';
  import Panel from '../components/Panel.svelte';
  import Select from '../components/Select.svelte';
  import Input from '../components/Input.svelte';
  import AlertBox from '../components/AlertBox.svelte';
  import ConfirmDialog from '../dialogs/ConfirmDialog.svelte';
  import { errorToast, successToast } from '../stores/toasts.svelte.js';

  let { workspaceId } = $props();

  let loading = $state(true);
  let bindings = $state([]);
  let candidates = $state([]);
  let scmConnections = $state([]);
  // Enabled LLM connections (global — not workspace-scoped). Any enabled
  // connection can back any workspace's binding; the binding stores the
  // connection id directly. A binding requires one: a run with no LLM can't
  // reach a model (the llm-proxy 403s a run with no LLM grant).
  let llmConnections = $state([]);

  // Add-form state.
  let addActingUserId = $state(null);
  let addSCMConnectionId = $state(null);
  // Repo slug is no longer typed by hand: it is derived from the repository
  // the admin picks under the chosen SCM connection (WI-90). The backend
  // deliberately derives remote URLs from the trusted SCM connection.
  let addRepositoryId = $state(null);
  let addRepoSlug = $state('');
  let addRepoBaseRef = $state('');
  let addLLMConnectionId = $state(null);
  let addTokenTTLMinutes = $state(60);
  let addMaxRunsPerDay = $state(0);
  let adding = $state(false);

  // Linked repositories for the currently-selected SCM connection.
  let linkedRepos = $state([]);
  let loadingRepos = $state(false);

  // Delete confirmation dialog.
  let deleteDialogOpen = $state(false);
  let pendingDelete = $state(null); // { id, label }

  // Per-binding "Test LLM" state, keyed by binding id.
  let testing = $state({}); // id -> bool
  let testResults = $state({}); // id -> { answer?: string, prompt?: string, error?: string }

  onMount(load);

  async function load() {
    loading = true;
    try {
      const [list, cands, conns, llmConns] = await Promise.all([
        agentBindings.listForWorkspace(workspaceId),
        agentBindings.getCandidates(workspaceId),
        api.workspaceSCM.getConnections(workspaceId).catch(() => []),
        // Global enabled LLM connections — any authenticated user may list the
        // slim public view. Fall back to an empty list rather than breaking
        // the whole form if it fails.
        api.llmProviders.getEnabled().catch(() => []),
      ]);
      bindings = list ?? [];
      candidates = cands ?? [];
      scmConnections = conns ?? [];
      llmConnections = llmConns ?? [];
    } catch (err) {
      console.error('Failed to load agent bindings:', err);
      errorToast(err?.message || 'Failed to load agent bindings');
    } finally {
      loading = false;
    }
  }

  let candidateOptions = $derived([
    { value: null, label: 'Pick an acting identity', disabled: true },
    ...(candidates || []).map((c) => ({
      value: c.user_id,
      label: `${c.name || c.username || `User #${c.user_id}`} — service user`,
      disabled: false,
    })),
  ]);

  let scmConnectionOptions = $derived([
    { value: null, label: '(none)', disabled: false },
    ...(scmConnections || []).map((c) => ({
      value: c.id,
      label: `${c.provider_name || c.provider_slug || `Connection #${c.id}`}`,
      disabled: false,
    })),
  ]);

  // LLM picker: every enabled connection (global — not workspace-scoped). The
  // binding stores the connection id directly. The connection is required, so
  // there is no "use defaults" option. The default connection is labelled as
  // such (the endpoint already returns it first).
  let llmOptions = $derived.by(() => {
    const opts = [{ value: null, label: 'Select an LLM connection', disabled: true }];
    for (const c of llmConnections || []) {
      const tags = [c.is_default ? 'default' : null, c.model].filter(Boolean).join(' · ');
      opts.push({
        value: c.id,
        label: tags ? `${c.name} (${tags})` : c.name || `Connection #${c.id}`,
        disabled: false,
      });
    }
    return opts;
  });

  // The Go coding-agent speaks OpenAI-compatible chat completions; the broker
  // rejects Anthropic-backed runs until translation lands. Warn at bind time.
  let selectedLLMIsAnthropic = $derived(
    (llmConnections || []).find((c) => c.id === addLLMConnectionId)?.provider_type === 'anthropic'
  );

  // Repository picker: populated from the linked repos of the selected
  // SCM connection. Disabled (with an explanatory placeholder) until a
  // connection is chosen.
  let repoOptions = $derived(
    !addSCMConnectionId
      ? [{ value: null, label: 'Select an SCM connection first', disabled: true }]
      : loadingRepos
        ? [{ value: null, label: 'Loading repositories…', disabled: true }]
        : linkedRepos.length === 0
          ? [{ value: null, label: 'No repositories linked to this connection', disabled: true }]
          : [
              { value: null, label: 'Pick a repository', disabled: true },
              ...linkedRepos.map((r) => ({
                value: r.id,
                label: r.repository_name || r.repository_url || `Repo #${r.id}`,
                disabled: false,
              })),
            ]
  );

  async function onConnectionChange(connId) {
    addSCMConnectionId = connId;
    // Reset the repo selection — the previous repo belonged to a
    // different connection.
    addRepositoryId = null;
    addRepoSlug = '';
    addRepoBaseRef = '';
    linkedRepos = [];
    if (!connId) return;
    loadingRepos = true;
    try {
      const repos = await api.workspaceSCM.getLinkedRepos(workspaceId, connId);
      linkedRepos = repos ?? [];
    } catch (err) {
      console.error('Failed to load repositories for connection:', err);
      errorToast(err?.message || 'Failed to load repositories');
      linkedRepos = [];
    } finally {
      loadingRepos = false;
    }
  }

  function onRepositoryChange(repoId) {
    addRepositoryId = repoId;
    const repo = linkedRepos.find((r) => r.id === repoId);
    // Mirror the linked repo's coordinates into the fields the create
    // request posts. The base ref defaults to the repo's default branch
    // but stays editable below.
    addRepoSlug = repo?.repository_name || '';
    addRepoBaseRef = repo?.default_branch || '';
  }

  // Resolve display names for the existing bindings table without an
  // extra fetch — candidates already covers everyone the admin can see.
  let displayActingUser = $derived((userId) => {
    const c = (candidates || []).find((c) => c.user_id === userId);
    return c?.name || c?.username || `User #${userId}`;
  });

  let displaySCMConnection = $derived((connId) => {
    if (!connId) return '—';
    const c = (scmConnections || []).find((c) => c.id === connId);
    return c?.provider_name || c?.provider_slug || `Connection #${connId}`;
  });

  let displayLLMConnection = $derived((connId) => {
    if (!connId) return '—';
    const c = (llmConnections || []).find((c) => c.id === connId);
    if (!c) return `Connection #${connId}`;
    return c.model ? `${c.name} · ${c.model}` : c.name;
  });

  let canAdd = $derived(!!addActingUserId && !!addLLMConnectionId && !adding);

  function resetForm() {
    addActingUserId = null;
    addSCMConnectionId = null;
    addRepositoryId = null;
    addRepoSlug = '';
    addRepoBaseRef = '';
    addLLMConnectionId = null;
    addTokenTTLMinutes = 60;
    addMaxRunsPerDay = 0;
    linkedRepos = [];
  }

  async function addBinding() {
    if (!canAdd) return;
    const body = {
      acting_user_id: addActingUserId,
      token_ttl_minutes: addTokenTTLMinutes || 60,
      max_runs_per_day: addMaxRunsPerDay || 0,
    };
    if (addSCMConnectionId) body.scm_connection_id = addSCMConnectionId;
    if (addLLMConnectionId) body.llm_connection_id = addLLMConnectionId;
    if (addRepoSlug.trim()) body.repo_slug = addRepoSlug.trim();
    if (addRepoBaseRef.trim()) body.repo_base_ref = addRepoBaseRef.trim();
    adding = true;
    try {
      await agentBindings.create(workspaceId, body);
      successToast('Agent binding created');
      resetForm();
      await load();
    } catch (err) {
      errorToast(err?.message || 'Failed to create binding');
      console.error('Failed to create binding:', err);
    } finally {
      adding = false;
    }
  }

  // Round-trip a quick prompt through the binding's LLM connection and show
  // the model's reply inline. Proves the connection (key + model) is reachable
  // before assigning real work.
  async function testBinding(b) {
    testing = { ...testing, [b.id]: true };
    testResults = { ...testResults, [b.id]: null };
    try {
      const res = await agentBindings.testLLM(workspaceId, b.id);
      testResults = {
        ...testResults,
        [b.id]: { answer: res?.answer || '(empty response)', prompt: res?.prompt },
      };
    } catch (err) {
      testResults = { ...testResults, [b.id]: { error: err?.message || 'LLM test failed' } };
    } finally {
      testing = { ...testing, [b.id]: false };
    }
  }

  function openDeleteDialog(binding) {
    pendingDelete = {
      id: binding.id,
      label: `${displayActingUser(binding.acting_user_id)}${binding.repo_slug ? ` (${binding.repo_slug})` : ''}`,
    };
    deleteDialogOpen = true;
  }

  async function confirmDelete() {
    if (!pendingDelete) return;
    try {
      await agentBindings.remove(workspaceId, pendingDelete.id);
      successToast('Agent binding removed');
      pendingDelete = null;
      await load();
    } catch (err) {
      errorToast(err?.message || 'Failed to remove binding');
      console.error('Failed to remove binding:', err);
    }
  }

  function cancelDelete() {
    pendingDelete = null;
  }
</script>

<div>
  <div class="flex items-start gap-3 mb-4">
    <Bot class="w-5 h-5 mt-1" style="color: var(--ds-icon);" />
    <div>
      <h3 class="text-base font-medium" style="color: var(--ds-text);">Agents</h3>
      <p class="text-sm mt-1" style="color: var(--ds-text-subtle);">
        Wire a centralized service user (allowlisted by a global admin) to a repo. When a work
        item gets assigned to that identity the orchestrator spawns a coding-agent run scoped to
        this workspace's SCM connection.
      </p>
    </div>
  </div>

  {#if loading}
    <Panel padding="spacious">
      <div class="flex items-center justify-center py-8">
        <Loader2 class="w-5 h-5 animate-spin" style="color: var(--ds-icon-subtle);" />
      </div>
    </Panel>
  {:else}
    <!-- Existing bindings -->
    <Panel padding="spacious">
      <h4 class="text-sm font-medium mb-3" style="color: var(--ds-text);">Configured bindings</h4>
      {#if bindings.length === 0}
        <p class="text-sm py-2" style="color: var(--ds-text-subtle);">
          No bindings yet. Add one below to enable assignee-driven runs in this workspace.
        </p>
      {:else}
        <div class="border rounded-md overflow-hidden" style="border-color: var(--ds-border);">
          <table class="w-full text-sm">
            <thead>
              <tr style="background-color: var(--ds-background-neutral);">
                <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Acting identity</th>
                <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Kind</th>
                <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Repo</th>
                <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">SCM</th>
                <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">LLM</th>
                <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Budget</th>
                <th class="px-3 py-2"></th>
              </tr>
            </thead>
            <tbody>
              {#each bindings as b (b.id)}
                <tr class="border-t" style="border-color: var(--ds-border);">
                  <td class="px-3 py-2" style="color: var(--ds-text);">{displayActingUser(b.acting_user_id)}</td>
                  <td class="px-3 py-2" style="color: var(--ds-text-subtle);">
                    {b.acting_user_kind === 'agent' ? 'Owned agent' : 'Centralized service user'}
                  </td>
                  <td class="px-3 py-2" style="color: var(--ds-text-subtle);">
                    {b.repo_slug ? `${b.repo_slug}${b.repo_base_ref ? ` @ ${b.repo_base_ref}` : ''}` : '—'}
                  </td>
                  <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{displaySCMConnection(b.scm_connection_id)}</td>
                  <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{displayLLMConnection(b.llm_connection_id)}</td>
                  <td class="px-3 py-2" style="color: var(--ds-text-subtle);">
                    {b.max_runs_per_day > 0 ? `${b.max_runs_per_day}/day` : 'unlimited'} · token {b.token_ttl_minutes}m
                  </td>
                  <td class="px-3 py-2 text-right whitespace-nowrap">
                    <button
                      type="button"
                      onclick={() => testBinding(b)}
                      disabled={testing[b.id] || !b.llm_connection_id}
                      class="inline-flex items-center justify-center p-1 rounded hover:opacity-80 disabled:opacity-40 disabled:cursor-not-allowed"
                      style="color: var(--ds-icon);"
                      title={b.llm_connection_id
                        ? 'Test the agent LLM with a quick prompt'
                        : 'No LLM connection on this binding to test'}
                      aria-label="Test LLM for {displayActingUser(b.acting_user_id)}"
                    >
                      {#if testing[b.id]}
                        <Loader2 class="w-4 h-4 animate-spin" />
                      {:else}
                        <FlaskConical class="w-4 h-4" />
                      {/if}
                    </button>
                    <button
                      type="button"
                      onclick={() => openDeleteDialog(b)}
                      class="inline-flex items-center justify-center p-1 rounded hover:opacity-80"
                      style="color: var(--ds-icon-danger);"
                      title="Remove binding"
                      aria-label="Remove binding for {displayActingUser(b.acting_user_id)}"
                    >
                      <Trash2 class="w-4 h-4" />
                    </button>
                  </td>
                </tr>
                {#if testResults[b.id]}
                  <tr style="border-color: var(--ds-border);">
                    <td colspan="7" class="px-3 pb-3">
                      {#if testResults[b.id].error}
                        <div
                          class="text-xs rounded p-2"
                          style="background-color: var(--ds-background-danger-subtle, var(--ds-background-neutral)); color: var(--ds-text-danger);"
                        >
                          ✗ {testResults[b.id].error}
                        </div>
                      {:else}
                        <div
                          class="text-xs rounded p-2"
                          style="background-color: var(--ds-background-neutral); color: var(--ds-text);"
                        >
                          <span style="color: var(--ds-text-subtle);">Model reply:</span>
                          {testResults[b.id].answer}
                        </div>
                      {/if}
                    </td>
                  </tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </Panel>

    <!-- Add form -->
    <div class="mt-4">
      <Panel padding="spacious">
        <h4 class="text-sm font-medium mb-3" style="color: var(--ds-text);">Add a binding</h4>
        {#if candidates.length === 0}
          <AlertBox variant="warning" message="No acting identities are available. Ask a global admin to create a service user (User management → Create user → Service user), enable centralized service users in Security settings, and allowlist it for this workspace." />
        {:else}
          <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label for="binding-acting-user" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">Acting identity</label>
              <Select id="binding-acting-user" bind:value={addActingUserId} options={candidateOptions} />
            </div>
            <div>
              <label for="binding-scm-connection" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">SCM connection (for git + PR auth)</label>
              <Select id="binding-scm-connection" bind:value={addSCMConnectionId} onchange={onConnectionChange} options={scmConnectionOptions} />
            </div>
            <div>
              <label for="binding-repository" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">Repository</label>
              <Select id="binding-repository" bind:value={addRepositoryId} onchange={onRepositoryChange} options={repoOptions} disabled={!addSCMConnectionId || loadingRepos} />
              <p class="text-xs mt-1" style="color: var(--ds-text-subtle);">Clone URL is derived from the selected SCM connection — the orchestrator never accepts a free-form remote URL.</p>
            </div>
            <div>
              <label for="binding-repo-base" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">Base ref</label>
              <Input id="binding-repo-base" bind:value={addRepoBaseRef} placeholder="main" />
            </div>
            <div>
              <label for="binding-llm" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">LLM connection (required)</label>
              <Select id="binding-llm" bind:value={addLLMConnectionId} options={llmOptions} />
              {#if llmConnections.length === 0}
                <p class="text-xs mt-1" style="color: var(--ds-text-danger);">No enabled LLM connections. Ask a global admin to add one under Admin → AI Connections.</p>
              {:else if selectedLLMIsAnthropic}
                <p class="text-xs mt-1" style="color: var(--ds-text-warning, var(--ds-text-subtle));">Anthropic connections aren't usable by the coding agent yet — it speaks OpenAI-compatible APIs. Pick an OpenAI-compatible provider (e.g. OpenRouter).</p>
              {/if}
            </div>
            <div>
              <label for="binding-ttl" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">Per-run token TTL (minutes)</label>
              <Input id="binding-ttl" type="number" min="5" max="1440" bind:value={addTokenTTLMinutes} />
            </div>
            <div>
              <label for="binding-budget" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">Max runs / day (0 = unlimited)</label>
              <Input id="binding-budget" type="number" min="0" bind:value={addMaxRunsPerDay} />
            </div>
          </div>
          <div class="mt-4 flex justify-end">
            <button
              type="button"
              onclick={addBinding}
              disabled={!canAdd}
              class="inline-flex items-center gap-2 px-4 py-2 rounded text-sm font-medium disabled:opacity-50"
              style="background-color: var(--ds-interactive); color: var(--ds-text-inverse);"
            >
              {#if adding}
                <Loader2 class="w-4 h-4 animate-spin" />
              {:else}
                <Plus class="w-4 h-4" />
              {/if}
              Add binding
            </button>
          </div>
        {/if}
      </Panel>
    </div>
  {/if}
</div>

<ConfirmDialog
  bind:show={deleteDialogOpen}
  variant="danger"
  title="Remove agent binding?"
  message={`Removing the binding for ${pendingDelete?.label ?? ''} stops the assignee-driven run trigger. In-flight runs continue to completion; future assignments produce no run until you re-create the binding.`}
  confirmText="Remove binding"
  onconfirm={confirmDelete}
  oncancel={cancelDelete}
/>
