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
  import UserPicker from '../pickers/UserPicker.svelte';
  import Select from '../components/Select.svelte';
  import Input from '../components/Input.svelte';
  import Label from '../components/Label.svelte';
  import Textarea from '../components/Textarea.svelte';
  import AlertBox from '../components/AlertBox.svelte';
  import SectionHeader from '../layout/SectionHeader.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Modal from '../dialogs/Modal.svelte';
  import ModalHeader from '../dialogs/ModalHeader.svelte';
  import DialogFooter from '../dialogs/DialogFooter.svelte';
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

  // Add/edit modal state. editingBinding is null for create, or the binding
  // being edited (only its persona/skills are mutable — the identity, repo,
  // LLM, and budget are fixed at create time, so edit shows them read-only).
  let showModal = $state(false);
  let editingBinding = $state(null);
  let formActingUserId = $state(null);
  let formTargetPoolId = $state(null); // null = local in-process runtime
  let formSCMConnectionId = $state(null);
  // Repo slug is no longer typed by hand: it is derived from the repository
  // the admin picks under the chosen SCM connection (WI-90). The backend
  // deliberately derives remote URLs from the trusted SCM connection.
  let formRepositoryId = $state(null);
  let formRepoSlug = $state('');
  let formRepoBaseRef = $state('');
  let formLLMConnectionId = $state(null);
  let formTokenTTLMinutes = $state(60);
  let formMaxRunsPerDay = $state(0);
  let formInstructions = $state('');
  let formSkillIds = $state([]);
  let saving = $state(false);

  // Workspace skills library (WI-258) for the attach pickers.
  let workspaceSkills = $state([]);

  function skillLabel(skill) {
    if (!skill) return '';
    return skill.enabled ? skill.name : `${skill.name} (disabled)`;
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

  // Shape the candidate service users for UserPicker (searchable combobox):
  // there can be hundreds, so a plain <select> doesn't scale. The endpoint
  // only returns the combined git display name; map it into first_name so
  // the trigger label and the search both work, with email shown beneath.
  let candidateUsers = $derived(
    (candidates || []).map((c) => ({
      id: c.user_id,
      first_name: c.name || c.username || `User #${c.user_id}`,
      last_name: '',
      email: c.email,
      username: c.username,
    }))
  );

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

  // Legacy direct Anthropic rows can still exist on upgraded instances, but
  // the coding-agent broker accepts OpenAI-compatible connections only.
  let selectedLLMIsDirectAnthropic = $derived(
    (llmConnections || []).find((c) => c.id === formLLMConnectionId)?.provider_type === 'anthropic'
  );

  // Repository picker: populated from the linked repos of the selected
  // SCM connection. Disabled (with an explanatory placeholder) until a
  // connection is chosen.
  let repoOptions = $derived(
    !formSCMConnectionId
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
    formSCMConnectionId = connId;
    // Reset the repo selection — the previous repo belonged to a
    // different connection.
    formRepositoryId = null;
    formRepoSlug = '';
    formRepoBaseRef = '';
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
    formRepositoryId = repoId;
    const repo = linkedRepos.find((r) => r.id === repoId);
    // Mirror the linked repo's coordinates into the fields the create
    // request posts. The base ref defaults to the repo's default branch
    // but stays editable below.
    formRepoSlug = repo?.repository_name || '';
    formRepoBaseRef = repo?.default_branch || '';
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

  // Create requires an identity + an LLM; edit (persona/skills only) is always
  // submittable.
  let canSubmit = $derived(
    saving ? false : editingBinding ? true : !!formActingUserId && !!formLLMConnectionId
  );

  function resetForm() {
    formActingUserId = null;
    formTargetPoolId = null;
    formSCMConnectionId = null;
    formRepositoryId = null;
    formRepoSlug = '';
    formRepoBaseRef = '';
    formLLMConnectionId = null;
    formTokenTTLMinutes = 60;
    formMaxRunsPerDay = 0;
    formInstructions = '';
    formSkillIds = [];
    linkedRepos = [];
  }

  function openCreate() {
    editingBinding = null;
    resetForm();
    showModal = true;
  }

  function openEdit(b) {
    editingBinding = b;
    resetForm();
    // Only the persona/skills are mutable; prime them from the binding.
    formInstructions = b.instructions || '';
    formSkillIds = [...(b.skill_ids || [])];
    showModal = true;
  }

  function closeModal() {
    showModal = false;
    editingBinding = null;
    resetForm();
  }

  async function submitModal() {
    if (!canSubmit) return;
    saving = true;
    try {
      if (editingBinding) {
        await agentBindings.updateAgentConfig(workspaceId, editingBinding.id, {
          instructions: formInstructions,
          skill_ids: formSkillIds,
        });
        successToast('Agent configuration saved');
      } else {
        const body = {
          acting_user_id: formActingUserId,
          token_ttl_minutes: formTokenTTLMinutes || 60,
          max_runs_per_day: formMaxRunsPerDay || 0,
        };
        if (formTargetPoolId) body.target_pool_id = formTargetPoolId;
        if (formSCMConnectionId) body.scm_connection_id = formSCMConnectionId;
        if (formLLMConnectionId) body.llm_connection_id = formLLMConnectionId;
        if (formRepoSlug.trim()) body.repo_slug = formRepoSlug.trim();
        if (formRepoBaseRef.trim()) body.repo_base_ref = formRepoBaseRef.trim();
        if (formInstructions.trim()) body.instructions = formInstructions.trim();
        if (formSkillIds.length) body.skill_ids = formSkillIds;
        await agentBindings.create(workspaceId, body);
        successToast('Agent binding created');
      }
      closeModal();
      await load();
    } catch (err) {
      errorToast(err?.message || 'Failed to save binding');
      console.error('Failed to save binding:', err);
    } finally {
      saving = false;
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

<!-- Dropdown row for the skill attach-picker in the modal. -->
{#snippet skillOption({ item: skill })}
  <div class="flex flex-col min-w-0">
    <span class="font-medium truncate">{skillLabel(skill)}</span>
    {#if skill.description}
      <span class="text-xs truncate" style="color: var(--ds-text-subtle);">{skill.description}</span>
    {/if}
  </div>
{/snippet}

<Panel padding="spacious">
  <SectionHeader
    title="Agent bindings"
    subtitle="Wire a centralized service user to a repo — assigning a work item to it spawns a coding-agent run."
  >
    {#snippet actions()}
      <Button
        size="sm"
        icon={Plus}
        onclick={openCreate}
        dataTestid="binding-add"
        keyboardHint="A"
        hotkeyConfig={{ key: toHotkeyString('agentBindings', 'add'), guard: () => !showModal }}
      >
        Add binding
      </Button>
    {/snippet}
  </SectionHeader>

  {#if loading}
    <div class="flex items-center justify-center py-8">
      <Loader2 class="w-5 h-5 animate-spin" style="color: var(--ds-icon-subtle);" />
    </div>
  {:else if bindings.length === 0}
    <EmptyState
      icon={Bot}
      title="No bindings yet"
      description="Add one to enable assignee-driven coding-agent runs in this workspace."
    >
      {#snippet action()}
        <!-- shortcut-guard-exempt: duplicate of the section-header "Add binding" action, which carries the A hotkey -->
        <Button size="sm" icon={Plus} onclick={openCreate}>Add binding</Button>
      {/snippet}
    </EmptyState>
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
                  onclick={() => openEdit(b)}
                  title="Persona & skills"
                  dataTestid="binding-edit-{b.id}"
                >
                  <Wand2 class="w-4 h-4" />
                </Button>
                <Button size="sm" variant="ghost" onclick={() => openDeleteDialog(b)} title="Remove binding">
                  <Trash2 class="w-4 h-4" style="color: var(--ds-text-danger);" />
                </Button>
              </td>
            </tr>
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

<!-- Add / edit binding modal. Create shows the full wiring form; edit only
     exposes the mutable persona + skills (identity/repo/LLM are fixed). -->
<Modal isOpen={showModal} onclose={closeModal} onSubmit={submitModal} submitDisabled={!canSubmit} maxWidth="max-w-2xl">
  {#snippet children(submitHint)}
    <ModalHeader
      title={editingBinding ? 'Edit binding' : 'Add agent binding'}
      icon={Bot}
      onclose={closeModal}
    />
    <div class="px-6 py-4" data-testid="binding-modal">
      {#if editingBinding}
        <!-- Immutable context for the binding being edited. -->
        <dl class="grid grid-cols-2 gap-x-4 gap-y-2 text-sm mb-4 p-3 rounded border" style="border-color: var(--ds-border); background-color: var(--ds-surface-raised);">
          <div>
            <dt class="text-xs" style="color: var(--ds-text-subtle);">Acting identity</dt>
            <dd style="color: var(--ds-text);">{displayActingUser(editingBinding.acting_user_id)}</dd>
          </div>
          <div>
            <dt class="text-xs" style="color: var(--ds-text-subtle);">Runs on</dt>
            <dd style="color: var(--ds-text);">{displayTarget(editingBinding.target_pool_id)}</dd>
          </div>
          <div>
            <dt class="text-xs" style="color: var(--ds-text-subtle);">Repo</dt>
            <dd style="color: var(--ds-text);">{editingBinding.repo_slug ? `${editingBinding.repo_slug}${editingBinding.repo_base_ref ? ` @ ${editingBinding.repo_base_ref}` : ''}` : '—'}</dd>
          </div>
          <div>
            <dt class="text-xs" style="color: var(--ds-text-subtle);">LLM</dt>
            <dd style="color: var(--ds-text);">{displayLLMConnection(editingBinding.llm_connection_id)}</dd>
          </div>
        </dl>

        <div class="space-y-3">
          <div>
            <Label for="binding-instructions" class="mb-1">Custom instructions (the agent's persona, appended to the standard prompt)</Label>
            <Textarea
              id="binding-instructions"
              bind:value={formInstructions}
              rows={4}
              size="small"
              placeholder="You are our release manager…"
            />
          </div>
          {#if workspaceSkills.length > 0}
            <div data-testid="binding-skills">
              <Label class="mb-1">Skills</Label>
              <BasePicker
                bind:value={formSkillIds}
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
        </div>
      {:else if candidates.length === 0}
        <AlertBox variant="warning" message="No acting identities are available. Ask a global admin to create a service user (User management → Create user → Service user), enable centralized service users in Security settings, and allowlist it for this workspace." />
      {:else}
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <Label for="binding-acting-user" required class="mb-1">Acting identity</Label>
            <UserPicker
              bind:value={formActingUserId}
              users={candidateUsers}
              placeholder="Pick a service user"
              class="w-full"
            />
          </div>
          <div>
            <Label for="binding-target-pool" class="mb-1">Runs on</Label>
            <Select id="binding-target-pool" bind:value={formTargetPoolId} options={targetPoolOptions} />
            <p class="text-xs mt-1" style="color: var(--ds-text-subtle);">
              {runnerPools.length === 0
                ? 'No runner pools available — runs use the local in-process runtime.'
                : 'Local runs the agent on this server; a pool dispatches to a registered remote runner.'}
            </p>
          </div>
          <div>
            <Label for="binding-scm-connection" class="mb-1">SCM connection (for git + PR auth)</Label>
            <Select id="binding-scm-connection" bind:value={formSCMConnectionId} onchange={onConnectionChange} options={scmConnectionOptions} />
          </div>
          <div>
            <Label for="binding-repository" class="mb-1">Repository</Label>
            <Select id="binding-repository" bind:value={formRepositoryId} onchange={onRepositoryChange} options={repoOptions} disabled={!formSCMConnectionId || loadingRepos} />
            <p class="text-xs mt-1" style="color: var(--ds-text-subtle);">Clone URL is derived from the selected SCM connection — the orchestrator never accepts a free-form remote URL.</p>
          </div>
          <div>
            <Label for="binding-repo-base" class="mb-1">Base ref</Label>
            <Input id="binding-repo-base" bind:value={formRepoBaseRef} placeholder="main" />
          </div>
          <div>
            <Label for="binding-llm" required class="mb-1">LLM connection</Label>
            <Select id="binding-llm" bind:value={formLLMConnectionId} options={llmOptions} />
            {#if llmConnections.length === 0}
              <p class="text-xs mt-1" style="color: var(--ds-text-danger);">No enabled LLM connections. Ask a global admin to add one under Admin → AI Connections.</p>
            {:else if selectedLLMIsDirectAnthropic}
              <p class="text-xs mt-1" style="color: var(--ds-text-warning, var(--ds-text-subtle));">Direct Anthropic connections are legacy and not usable by the coding agent. Pick an OpenAI-compatible provider such as OpenRouter.</p>
            {/if}
          </div>
          <div>
            <Label for="binding-ttl" class="mb-1">Per-run token TTL (minutes)</Label>
            <Input id="binding-ttl" type="number" min="5" max="1440" bind:value={formTokenTTLMinutes} />
          </div>
          <div>
            <Label for="binding-budget" class="mb-1">Max runs / day (0 = unlimited)</Label>
            <Input id="binding-budget" type="number" min="0" bind:value={formMaxRunsPerDay} />
          </div>
        </div>
        <!-- Persona + skills (WI-258): appended to the run's standard prompt. -->
        <div class="mt-4">
          <Label for="binding-instructions" class="mb-1">Custom instructions (optional persona — "You are our release manager…")</Label>
          <Textarea
            id="binding-instructions"
            bind:value={formInstructions}
            rows={3}
            size="small"
            placeholder="Appended to the standard agent prompt as the agent's role. The operational rules (commit, comment, no push) stay in place."
          />
        </div>
        {#if workspaceSkills.length > 0}
          <div class="mt-3" data-testid="binding-skills">
            <Label class="mb-1">Skills</Label>
            <BasePicker
              bind:value={formSkillIds}
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
      {/if}
    </div>
    <DialogFooter
      onCancel={closeModal}
      onConfirm={submitModal}
      confirmLabel={editingBinding ? 'Save changes' : 'Add binding'}
      disabled={!canSubmit}
      loading={saving}
      confirmTestid="binding-save"
      showKeyboardHint
      confirmKeyboardHint={submitHint}
    />
  {/snippet}
</Modal>

<ConfirmDialog
  bind:show={deleteDialogOpen}
  variant="danger"
  title="Remove agent binding?"
  message={`Removing the binding for ${pendingDelete?.label ?? ''} stops the assignee-driven run trigger. In-flight runs continue to completion; future assignments produce no run until you re-create the binding.`}
  confirmText="Remove binding"
  onconfirm={confirmDelete}
  oncancel={cancelDelete}
/>
