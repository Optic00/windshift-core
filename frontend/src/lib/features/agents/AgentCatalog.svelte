<script>
  import { onMount } from 'svelte';
  import {
    IconBolt as Bolt,
    IconCode as Code,
    IconMessage as Message,
    IconActivity as Activity,
    IconPlus as Plus,
    IconUserStar as AgentIcon,
  } from '@tabler/icons-svelte-runes';
  import { agentBindings, agentRuns } from '../../api.js';
  import { workspacePermissions } from '../../stores';
  import PageHeader from '../../layout/PageHeader.svelte';
  import Avatar from '../../components/Avatar.svelte';
  import Badge from '../../components/Badge.svelte';
  import Button from '../../components/Button.svelte';
  import Card from '../../components/Card.svelte';
  import SearchInput from '../../components/SearchInput.svelte';
  import StateDisplay from '../../components/StateDisplay.svelte';
  import { getShortcutDisplay, toHotkeyString } from '../../utils/keyboardShortcuts.js';

  let { workspaceId } = $props();

  let agents = $state([]);
  let loading = $state(true);
  let error = $state('');
  let query = $state('');
  let recentRunByAgent = $state({});

  const canAdmin = $derived(workspacePermissions.canAdminWorkspace(workspaceId));
  const visibleAgents = $derived.by(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return agents;
    return agents.filter((agent) =>
      [agent.name, agent.handle, agent.purpose, agent.profile_type]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(needle))
    );
  });

  onMount(loadAgents);

  async function loadAgents() {
    loading = true;
    error = '';
    try {
      const [result, recentRuns] = await Promise.all([
        agentBindings.listCatalog(workspaceId),
        agentRuns.listForWorkspace(workspaceId, { limit: 100 }).catch(() => []),
      ]);
      agents = Array.isArray(result) ? result : [];
      const nextRecent = {};
      for (const run of recentRuns || []) {
        const key = String(run.binding_id || '');
        if (key && !nextRecent[key]) nextRecent[key] = run;
      }
      recentRunByAgent = nextRecent;
    } catch (err) {
      error = err.message || 'Agents could not be loaded.';
    } finally {
      loading = false;
    }
  }

  function availabilityVariant(value) {
    if (value === 'ready') return 'success';
    if (value === 'offline' || value === 'needs_setup' || value === 'draft') return 'warning';
    if (value === 'invalid') return 'danger';
    return 'neutral';
  }

  function availabilityLabel(value) {
    return {
      ready: 'Ready',
      offline: 'Offline · queues work',
      needs_setup: 'Needs setup',
      draft: 'Draft',
      paused: 'Paused',
      invalid: 'Invalid',
      archived: 'Archived',
    }[value] || 'Unavailable';
  }

  function typeLabel(value) {
    if (value === 'standard') return 'Standard';
    if (value === 'coding') return 'Coding';
    return 'Legacy';
  }

  function runtimeLabel(value) {
    return {
      windshift: 'Built-in Windshift runtime',
      authorized_runner: 'Authorized coding runner',
      legacy_local: 'Legacy local runtime',
    }[value] || value;
  }

  function identityLabel(value) {
    return {
      workspace_managed: 'Workspace managed',
      centralized_service: 'Central service identity',
      user_owned: 'User-owned identity',
    }[value] || value;
  }
</script>

<section
  class="min-h-full px-4 py-6 sm:px-6 lg:px-8"
  style="background-color: var(--ds-surface);"
  data-testid="agent-catalog"
>
  <div class="mx-auto max-w-7xl space-y-6">
    <PageHeader
      icon={AgentIcon}
      title="Agents"
      subtitle="Specialists available in this workspace"
    >
      {#snippet actions()}
        {#if canAdmin}
          <Button
            href={`/workspaces/${workspaceId}/agents/new`}
            variant="primary"
            size="medium"
            icon={Plus}
            keyboardHint={getShortcutDisplay('agents', 'add')}
            hotkeyConfig={{ key: toHotkeyString('agents', 'add') }}
            dataTestid="agent-catalog-manage"
          >
            Create agent
          </Button>
        {/if}
      {/snippet}
    </PageHeader>

    <div class="max-w-md">
      <SearchInput
        bind:value={query}
        placeholder="Search agents"
        dataTestid="agent-catalog-search"
      />
    </div>

    {#if loading}
      <StateDisplay type="loading" message="Loading workspace agents…" class="py-20" />
    {:else if error}
      <StateDisplay
        type="error"
        title="Agents could not be loaded"
        message={error}
        onRetry={loadAgents}
        class="py-20"
      />
    {:else if agents.length === 0}
      <Card variant="outlined" padding="spacious">
        <StateDisplay
          type="empty"
          icon={AgentIcon}
          title="No agents yet"
          description={canAdmin
            ? 'Create a Standard or Coding specialist for this workspace.'
            : 'A workspace administrator can add specialists here.'}
        />
        {#if canAdmin}
          <div class="mt-4 flex justify-center">
            <Button
              href={`/workspaces/${workspaceId}/agents/new`}
              variant="primary"
              icon={Plus}
              dataTestid="agent-catalog-empty-manage"
            >
              Create agent
            </Button>
          </div>
        {/if}
      </Card>
    {:else if visibleAgents.length === 0}
      <Card variant="outlined" padding="spacious">
        <StateDisplay
          type="empty"
          title="No matching agents"
          description="Try another name, handle, purpose, or profile type."
        />
      </Card>
    {:else}
      <div class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
        {#each visibleAgents as agent (agent.id)}
          <Card
            variant="raised"
            padding="default"
            hoverable
            href={`/workspaces/${workspaceId}/agents/${agent.id}`}
            dataTestid="agent-catalog-card"
            class="flex min-h-64 flex-col"
          >
            <div class="flex items-start gap-3">
              <Avatar
                src={agent.avatar_url}
                name={agent.name || agent.handle}
                size="lg"
                variant={agent.profile_type === 'coding' ? 'purple' : 'blue'}
                ring
              />
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <h2 class="truncate text-base font-semibold" style="color: var(--ds-text);">
                    {agent.name || agent.handle}
                  </h2>
                  <Badge variant={availabilityVariant(agent.availability)}>
                    {availabilityLabel(agent.availability)}
                  </Badge>
                </div>
                {#if agent.handle}
                  <p class="truncate text-sm" style="color: var(--ds-text-subtle);">
                    @{agent.handle}
                  </p>
                {/if}
              </div>
            </div>

            <p class="mt-4 line-clamp-3 min-h-15 text-sm" style="color: var(--ds-text-subtle);">
              {agent.purpose || 'A workspace specialist configured by your administrators.'}
            </p>

            <div class="mt-4 space-y-2 text-sm">
              <div class="flex items-center gap-2" style="color: var(--ds-text-subtle);">
                {#if agent.profile_type === 'coding'}
                  <Code class="h-4 w-4" />
                {:else}
                  <Bolt class="h-4 w-4" />
                {/if}
                <span>{typeLabel(agent.profile_type)} · {runtimeLabel(agent.runtime)}</span>
              </div>
              <div class="flex items-center gap-2" style="color: var(--ds-text-subtle);">
                <AgentIcon class="h-4 w-4" />
                <span>
                  {identityLabel(agent.identity_class)}
                  {agent.owner_name ? ` · Owned by ${agent.owner_name}` : ''}
                </span>
              </div>
              <div class="flex items-center gap-2" style="color: var(--ds-text-subtle);">
                <Activity class="h-4 w-4" />
                <span>{agent.model_summary || 'Model unavailable'}</span>
              </div>
              <div class="flex items-center gap-2" style="color: var(--ds-text-subtle);">
                <Message class="h-4 w-4" />
                <span>
                  {agent.profile_type === 'standard'
                    ? 'Assignment, mentions, and workspace chat'
                    : 'Direct assignment and mentions'}
                </span>
              </div>
              {#if recentRunByAgent[String(agent.id)]}
                {@const latestRun = recentRunByAgent[String(agent.id)]}
                <div class="flex items-center gap-2" style="color: var(--ds-text-subtle);">
                  <Activity class="h-4 w-4" />
                  <span>
                    Last run {latestRun.status} ·
                    {new Date(latestRun.updated_at || latestRun.created_at).toLocaleDateString()}
                  </span>
                </div>
              {/if}
            </div>

            <div class="mt-auto pt-4">
              <span class="text-xs" style="color: var(--ds-text-subtlest);">
                View profile · Version {agent.profile_version}
              </span>
            </div>
          </Card>
        {/each}
      </div>
    {/if}
  </div>
</section>
