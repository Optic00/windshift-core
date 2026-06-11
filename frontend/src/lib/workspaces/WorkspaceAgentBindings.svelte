<script>
  // Workspace agent bindings (WI-88). The page workspace admins use to
  // wire up coding-agent runners: pick an acting identity (an
  // allowlisted centralized service user), point it at a repo via
  // one of the workspace's SCM connections, set token budget knobs.
  // Backend chokepoint re-validates the identity at create time; the
  // candidates endpoint just keeps the picker honest.

  import { onDestroy, onMount, untrack } from 'svelte';
  import { Bot, FlaskConical, Loader2, Plus, Trash2, Wand2 } from '@lucide/svelte';
  import { agentBindings, agentRuns, agentSkills, api } from '../api.js';
  import Panel from '../components/Panel.svelte';
  import Button from '../components/Button.svelte';
  import { BasePicker } from '../pickers';
  import Select from '../components/Select.svelte';
  import Input from '../components/Input.svelte';
  import Textarea from '../components/Textarea.svelte';
  import AlertBox from '../components/AlertBox.svelte';
  import ConfirmDialog from '../dialogs/ConfirmDialog.svelte';
  import { errorToast, successToast } from '../stores/toasts.svelte.js';
  import { toHotkeyString } from '../utils/keyboardShortcuts.js';

  // skillsVersion: bumped by the parent when the skills panel below this one
  // creates/edits/deletes a skill, so the attach-pickers here don't go stale.
  let { workspaceId, skillsVersion = 0 } = $props();

  let loading = $state(true);
  let bindings = $state([]);
  let candidates = $state([]);
  let scmConnections = $state([]);
  // Enabled LLM connections (global — not workspace-scoped). Any enabled
  // connection can back any workspace's binding; the binding stores the
  // connection id directly. A binding requires one: a run with no LLM can't
  // reach a model (the llm-proxy 403s a run with no LLM grant).
  let llmConnections = $state([]);
  // Runner pools (action_capabilities of type runner_pool) this workspace may
  // dispatch to. A binding runs on a pool (remote) or the local in-process
  // runtime (null target).
  let runnerPools = $state([]);

  // Add-form state.
  let addActingUserId = $state(null);
  let addTargetPoolId = $state(null); // null = local in-process runtime
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
  let addInstructions = $state('');
  let addSkillIds = $state([]);
  let adding = $state(false);

  // Workspace skills library (WI-258) for the attach pickers.
  let workspaceSkills = $state([]);

  // Per-binding agent-config editor (instructions + skills), keyed open by id.
  let configFor = $state(null);
  let configInstructions = $state('');
  let configSkillIds = $state([]);
  let configSaving = $state(false);

  function skillLabel(skill) {
    if (!skill) return '';
    return skill.enabled ? skill.name : `${skill.name} (disabled)`;
  }

  function openConfig(b) {
    configFor = b.id;
    configInstructions = b.instructions || '';
    configSkillIds = [...(b.skill_ids || [])];
  }

  async function saveConfig() {
    if (configFor == null) return;
    configSaving = true;
    try {
      await agentBindings.updateAgentConfig(workspaceId, configFor, {
        instructions: configInstructions,
        skill_ids: configSkillIds,
      });
      successToast('Agent configuration saved');
      configFor = null;
      await load();
    } catch (err) {
      errorToast(err?.message || 'Failed to save agent configuration');
      console.error('Failed to save agent config:', err);
    } finally {
      configSaving = false;
    }
  }

  // Linked repositories for the currently-selected SCM connection.
  let linkedRepos = $state([]);
  let loadingRepos = $state(false);

  // Delete confirmation dialog.
  let deleteDialogOpen = $state(false);
  let pendingDelete = $state(null); // { id, label }

  // Per-binding "Test run" state, keyed by binding id.
  let testing = $state({}); // id -> bool
  let testResults = $state({}); // id -> { runId?, status?, lines?: string[], error?: string }
  // Cancellation tokens so a new test (or unmount) abandons a stale poll loop.
  const watchTokens = {}; // id -> Symbol
  onDestroy(() => {
    for (const id of Object.keys(watchTokens)) delete watchTokens[id];
  });

  const TEST_RUN_POLL_MS = 1500;
  const TEST_RUN_TIMEOUT_MS = 5 * 60 * 1000;
  const TEST_RUN_TERMINAL = ['succeeded', 'failed', 'canceled', 'killed'];

  function isTerminalTestStatus(status) {
    return TEST_RUN_TERMINAL.includes(status);
  }

  function testRunStatusLabel(status) {
    switch (status) {
      case 'starting':
        return 'starting…';
      case 'queued':
        return 'queued…';
      case 'running':
        return 'running…';
      case 'succeeded':
        return '✓ succeeded';
      case 'failed':
        return '✗ failed';
      case 'canceled':
        return 'canceled';
      case 'killed':
        return 'killed';
      case 'timeout':
        return 'still running (stopped watching)';
      default:
        return status || '…';
    }
  }

  function testRunStatusColor(status) {
    if (status === 'succeeded') return 'var(--ds-text-success, var(--ds-text))';
    if (status === 'failed' || status === 'killed') return 'var(--ds-text-danger)';
    return 'var(--ds-text-subtle)';
  }

  // Render one run event as a transcript line, or null to drop it. The runner
  // wraps the windshift-agent's NDJSON in "stdout" events; the inner payload's
  // own `type` is the agent's event vocabulary. We surface the canonical final
  // `message` and tool calls, and DROP the streaming `content` deltas — the
  // agent emits the final answer both ways (agent.go: content stream + a final
  // message), so rendering both is what double-printed the reply. Server-level
  // lifecycle/status events are conveyed by the status badge, not the
  // transcript.
  function eventText(ev) {
    if (ev.type === 'lifecycle') return null;
    let payload;
    try {
      payload = JSON.parse(ev.payload_json);
    } catch {
      return ev.payload_json; // non-JSON line, show as-is
    }
    switch (payload.type) {
      case 'message':
        // The canonical final assistant message.
        return typeof payload.text === 'string' ? payload.text : null;
      case 'content':
        // Streaming deltas — duplicate of `message`; drop to avoid doubling.
        return null;
      case 'tool_start':
        // Show the command compactly: "$ ls -1 /workspace" for bash, else the tool.
        return payload.args?.cmd ? `$ ${payload.args.cmd}` : `→ ${payload.tool || 'tool'}`;
      case 'tool_done':
        return null; // output is large + already implied by the next message
      case 'error':
        return payload.message ? `⚠ ${payload.message}` : null;
      case 'lifecycle':
      case 'starting':
      case 'session_idle':
        return null; // status-level, not transcript content
      default:
        // Unknown shape: surface a text/message field if present, else drop.
        return typeof (payload.text ?? payload.message ?? payload.line) === 'string'
          ? (payload.text ?? payload.message ?? payload.line)
          : null;
    }
  }

  function appendLines(id, events) {
    const fresh = (events || [])
      .map(eventText)
      .filter((t) => typeof t === 'string' && t.trim() !== '');
    if (!fresh.length) return;
    const cur = testResults[id] || {};
    testResults = {
      ...testResults,
      [id]: { ...cur, lines: [...(cur.lines || []), ...fresh].slice(-40) },
    };
  }

  onMount(load);

  // Refresh just the skills list when the sibling skills panel reports a
  // change (initial value is covered by load()). The untrack'd snapshot is
  // intentional: it pins the mount-time version so the effect only fires
  // for changes after mount.
  let lastSkillsVersion = untrack(() => skillsVersion);
  $effect(() => {
    if (skillsVersion === lastSkillsVersion) return;
    lastSkillsVersion = skillsVersion;
    agentSkills
      .listForWorkspace(workspaceId)
      .then((skills) => (workspaceSkills = skills ?? []))
      .catch(() => {});
  });

  async function load() {
    loading = true;
    try {
      const [list, cands, conns, llmConns, pools, skills] = await Promise.all([
        agentBindings.listForWorkspace(workspaceId),
        agentBindings.getCandidates(workspaceId),
        api.workspaceSCM.getConnections(workspaceId).catch(() => []),
        // Global enabled LLM connections — any authenticated user may list the
        // slim public view. Fall back to an empty list rather than breaking
        // the whole form if it fails.
        api.llmProviders.getEnabled().catch(() => []),
        // Runner pools this workspace can target (empty if none / not allowed).
        api.actionCapabilities.getForWorkspace(workspaceId, 'runner_pool').catch(() => []),
        // Skills library for the attach pickers (WI-258).
        agentSkills.listForWorkspace(workspaceId).catch(() => []),
      ]);
      bindings = list ?? [];
      candidates = cands ?? [];
      scmConnections = conns ?? [];
      llmConnections = llmConns ?? [];
      runnerPools = pools ?? [];
      workspaceSkills = skills ?? [];
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

  // Where a binding runs: a named pool, or the local in-process runtime.
  let displayTarget = $derived((poolId) => {
    if (!poolId) return 'Local';
    const p = (runnerPools || []).find((p) => p.id === poolId);
    return p?.name || `Pool #${poolId}`;
  });

  // "Run on" options: local runtime (null) + each runner pool this workspace
  // may target.
  let targetPoolOptions = $derived([
    { value: null, label: 'Local (in-process)' },
    ...(runnerPools || []).map((p) => ({ value: p.id, label: `Pool: ${p.name}` })),
  ]);

  let canAdd = $derived(!!addActingUserId && !!addLLMConnectionId && !adding);

  function resetForm() {
    addActingUserId = null;
    addTargetPoolId = null;
    addSCMConnectionId = null;
    addRepositoryId = null;
    addRepoSlug = '';
    addRepoBaseRef = '';
    addLLMConnectionId = null;
    addTokenTTLMinutes = 60;
    addMaxRunsPerDay = 0;
    addInstructions = '';
    addSkillIds = [];
    linkedRepos = [];
  }

  async function addBinding() {
    if (!canAdd) return;
    const body = {
      acting_user_id: addActingUserId,
      token_ttl_minutes: addTokenTTLMinutes || 60,
      max_runs_per_day: addMaxRunsPerDay || 0,
    };
    if (addTargetPoolId) body.target_pool_id = addTargetPoolId;
    if (addSCMConnectionId) body.scm_connection_id = addSCMConnectionId;
    if (addLLMConnectionId) body.llm_connection_id = addLLMConnectionId;
    if (addRepoSlug.trim()) body.repo_slug = addRepoSlug.trim();
    if (addRepoBaseRef.trim()) body.repo_base_ref = addRepoBaseRef.trim();
    if (addInstructions.trim()) body.instructions = addInstructions.trim();
    if (addSkillIds.length) body.skill_ids = addSkillIds;
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

  // Provision a real (but ephemeral, item-less) coding-agent container run for
  // the binding and watch it to completion — proving the full chain: the model
  // is reachable, the repo checks out into a worktree, and the agent can read
  // its files. The run can never push a branch or open a PR (server marks it
  // ephemeral); the agent's prompt is read-only.
  async function testBinding(b) {
    const token = Symbol('test-run');
    watchTokens[b.id] = token;
    testing = { ...testing, [b.id]: true };
    testResults = { ...testResults, [b.id]: { status: 'starting', lines: [] } };
    try {
      const { run_id: runId } = await agentBindings.testRun(workspaceId, b.id);
      if (watchTokens[b.id] !== token) return;
      testResults = { ...testResults, [b.id]: { runId, status: 'queued', lines: [] } };

      let afterId = 0;
      const deadline = Date.now() + TEST_RUN_TIMEOUT_MS;
      while (watchTokens[b.id] === token) {
        const events = await agentRuns.listEventsAfter(runId, afterId, 200);
        if (watchTokens[b.id] !== token) return;
        if (events?.length) {
          afterId = events[events.length - 1].id;
          appendLines(b.id, events);
        }

        const run = await agentRuns.get(runId);
        if (watchTokens[b.id] !== token) return;
        const cur = testResults[b.id] || {};
        testResults = { ...testResults, [b.id]: { ...cur, status: run.status, error: run.error || '' } };

        if (isTerminalTestStatus(run.status)) {
          // Final drain so the agent's last output isn't lost to timing.
          const tail = await agentRuns.listEventsAfter(runId, afterId, 200);
          if (watchTokens[b.id] === token && tail?.length) appendLines(b.id, tail);
          break;
        }
        if (Date.now() > deadline) {
          const c = testResults[b.id] || {};
          testResults = {
            ...testResults,
            [b.id]: {
              ...c,
              status: 'timeout',
              error: 'Stopped watching after 5 min — see the Agent runs panel for the rest.',
            },
          };
          break;
        }
        await new Promise((r) => setTimeout(r, TEST_RUN_POLL_MS));
      }
    } catch (err) {
      if (watchTokens[b.id] === token) {
        const cur = testResults[b.id] || {};
        testResults = {
          ...testResults,
          [b.id]: { ...cur, status: cur.status || 'error', error: err?.message || 'Test run failed to start' },
        };
      }
    } finally {
      if (watchTokens[b.id] === token) testing = { ...testing, [b.id]: false };
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

<!-- Dropdown row for the skill attach-pickers (config editor + add form). -->
{#snippet skillOption({ item: skill })}
  <div class="flex flex-col min-w-0">
    <span class="font-medium truncate">{skillLabel(skill)}</span>
    {#if skill.description}
      <span class="text-xs truncate" style="color: var(--ds-text-subtle);">{skill.description}</span>
    {/if}
  </div>
{/snippet}

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
                <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Runs on</th>
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
                  <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{displayTarget(b.target_pool_id)}</td>
                  <td class="px-3 py-2" style="color: var(--ds-text-subtle);">
                    {b.repo_slug ? `${b.repo_slug}${b.repo_base_ref ? ` @ ${b.repo_base_ref}` : ''}` : '—'}
                  </td>
                  <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{displaySCMConnection(b.scm_connection_id)}</td>
                  <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{displayLLMConnection(b.llm_connection_id)}</td>
                  <td class="px-3 py-2" style="color: var(--ds-text-subtle);">
                    {b.max_runs_per_day > 0 ? `${b.max_runs_per_day}/day` : 'unlimited'} · token {b.token_ttl_minutes}m
                  </td>
                  <td class="px-3 py-2 text-right whitespace-nowrap">
                    <Button
                      size="sm"
                      variant="ghost"
                      onclick={() => testBinding(b)}
                      disabled={testing[b.id] || !b.llm_connection_id || !b.repo_slug || !!b.target_pool_id}
                      loading={testing[b.id]}
                      icon={FlaskConical}
                      title={!b.llm_connection_id
                        ? 'No LLM connection on this binding to test'
                        : !b.repo_slug
                          ? 'This binding has no repo — a test run needs one to check out'
                          : b.target_pool_id
                            ? 'Test runs execute on the local runtime and are not supported for runner-pool bindings — assign a real work item to verify the pool'
                            : 'Test run: provision a real container, check out the repo, have the agent list its files'}
                    />
                    <Button
                      size="sm"
                      variant="ghost"
                      onclick={() => (configFor === b.id ? (configFor = null) : openConfig(b))}
                      title="Persona & skills"
                      dataTestid="binding-agent-config-{b.id}"
                    >
                      <Wand2 class="w-4 h-4" />
                    </Button>
                    <Button size="sm" variant="ghost" onclick={() => openDeleteDialog(b)} title="Remove binding">
                      <Trash2 class="w-4 h-4" style="color: var(--ds-text-danger);" />
                    </Button>
                  </td>
                </tr>
                {#if configFor === b.id}
                  <tr style="border-color: var(--ds-border);">
                    <td colspan="8" class="px-3 pb-3">
                      <div class="rounded border p-3 space-y-3" style="border-color: var(--ds-border);" data-testid="binding-agent-config-editor">
                        <div>
                          <label for="config-instructions-{b.id}" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">
                            Custom instructions (appended to the standard prompt as the agent's role)
                          </label>
                          <Textarea id="config-instructions-{b.id}" bind:value={configInstructions} rows={3} size="small" />
                        </div>
                        {#if workspaceSkills.length > 0}
                          <div data-testid="binding-agent-config-skills">
                            <span class="block text-xs mb-1" style="color: var(--ds-text-subtle);">Skills</span>
                            <BasePicker
                              bind:value={configSkillIds}
                              items={workspaceSkills}
                              multiple={true}
                              placeholder="Attach skills…"
                              searchFields={['name', 'description']}
                              getValue={(s) => s?.id}
                              getLabel={skillLabel}
                              itemSnippet={skillOption}
                            />
                          </div>
                        {:else}
                          <p class="text-xs" style="color: var(--ds-text-subtle);">No skills in this workspace yet — create them in the Agent skills panel below.</p>
                        {/if}
                        <div class="flex justify-end gap-2">
                          <Button size="sm" variant="secondary" onclick={() => (configFor = null)}>Cancel</Button>
                          <Button
                            size="sm"
                            variant="primary"
                            onclick={saveConfig}
                            disabled={configSaving}
                            loading={configSaving}
                            dataTestid="binding-agent-config-save"
                          >
                            Save
                          </Button>
                        </div>
                      </div>
                    </td>
                  </tr>
                {/if}
                {#if testResults[b.id]}
                  <tr style="border-color: var(--ds-border);">
                    <td colspan="8" class="px-3 pb-3">
                      {#if testResults[b.id].error && !testResults[b.id].lines?.length}
                        <div
                          class="text-xs rounded p-2"
                          style="background-color: var(--ds-background-danger-subtle, var(--ds-background-neutral)); color: var(--ds-text-danger);"
                        >
                          ✗ {testResults[b.id].error}
                        </div>
                      {:else}
                        <div
                          class="text-xs rounded p-2 space-y-1"
                          style="background-color: var(--ds-background-neutral); color: var(--ds-text);"
                        >
                          <div class="flex items-center gap-2" style="color: var(--ds-text-subtle);">
                            <span>Test run{testResults[b.id].runId ? ` #${testResults[b.id].runId}` : ''}:</span>
                            <span style="color: {testRunStatusColor(testResults[b.id].status)};">
                              {testRunStatusLabel(testResults[b.id].status)}
                            </span>
                            {#if !isTerminalTestStatus(testResults[b.id].status)}
                              <Loader2 class="w-3 h-3 animate-spin" />
                            {/if}
                          </div>
                          {#if testResults[b.id].lines?.length}
                            <!-- XSS: this is agent + repo-derived output (e.g. a
                                 file named "<img onerror=...>"). It MUST stay a
                                 plain {…} interpolation so Svelte HTML-escapes it.
                                 Never switch this to {@html}/markdown without
                                 routing through sanitizeHtml (utils/sanitize). -->
                            <pre
                              class="whitespace-pre-wrap break-words rounded p-2 m-0"
                              style="background-color: var(--ds-surface-sunken, var(--ds-background)); color: var(--ds-text); max-height: 12rem; overflow: auto;"
                            >{testResults[b.id].lines.join('\n')}</pre>
                          {/if}
                          {#if testResults[b.id].error}
                            <div style="color: var(--ds-text-danger);">✗ {testResults[b.id].error}</div>
                          {/if}
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
              <label for="binding-target-pool" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">Runs on</label>
              <Select id="binding-target-pool" bind:value={addTargetPoolId} options={targetPoolOptions} />
              <p class="text-xs mt-1" style="color: var(--ds-text-subtle);">
                {runnerPools.length === 0
                  ? 'No runner pools available — runs use the local in-process runtime.'
                  : 'Local runs the agent on this server; a pool dispatches to a registered remote runner.'}
              </p>
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
          <!-- Persona + skills (WI-258): appended to the run's standard prompt. -->
          <div class="mt-4">
            <label for="binding-instructions" class="block text-xs mb-1" style="color: var(--ds-text-subtle);">
              Custom instructions (optional persona — "You are our release manager…")
            </label>
            <Textarea
              id="binding-instructions"
              bind:value={addInstructions}
              rows={3}
              size="small"
              placeholder="Appended to the standard agent prompt as the agent's role. The operational rules (commit, comment, no push) stay in place."
            />
          </div>
          {#if workspaceSkills.length > 0}
            <div class="mt-3 max-w-xl" data-testid="binding-add-skills">
              <span class="block text-xs mb-1" style="color: var(--ds-text-subtle);">Skills</span>
              <BasePicker
                bind:value={addSkillIds}
                items={workspaceSkills}
                multiple={true}
                placeholder="Attach skills…"
                searchFields={['name', 'description']}
                getValue={(s) => s?.id}
                getLabel={skillLabel}
                itemSnippet={skillOption}
              />
            </div>
          {/if}
          <div class="mt-4 flex justify-end">
            <Button
              variant="primary"
              icon={Plus}
              onclick={addBinding}
              disabled={!canAdd}
              loading={adding}
              keyboardHint="A"
              hotkeyConfig={{ key: toHotkeyString('agentBindings', 'add'), guard: () => canAdd }}
            >
              Add binding
            </Button>
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
