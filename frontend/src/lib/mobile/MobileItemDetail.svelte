<script>
  import { Star, Play, Loader, ChevronDown, ChevronRight, GitPullRequest, Bot } from '@lucide/svelte';
  import { api } from '../api.js';
  import { agentRuns } from '../api/agentRuns.js';
  import { navigate } from '../router.js';
  import { timerStore } from '../stores/timerStore.svelte.js';
  import { renderMarkdown } from '../utils/render-markdown.js';
  import { formatDate } from '../utils/dateFormatter.js';
  import { formatItemKey } from '../utils/itemKey.js';
  import MobileHeader from './MobileHeader.svelte';
  import MobileItemRow from './MobileItemRow.svelte';
  import StatusPill from '../components/StatusPill.svelte';
  import Comments from '../features/items/Comments.svelte';
  import ItemSCMLinks from '../features/items/ItemSCMLinks.svelte';
  import ItemAgentLog from '../features/items/ItemAgentLog.svelte';
  import BasePicker from '../pickers/BasePicker.svelte';
  import UserPicker from '../pickers/UserPicker.svelte';
  import Avatar from '../components/Avatar.svelte';

  let { itemId } = $props();

  let item = $state(null);
  let loading = $state(true);
  let errored = $state(false);
  let transitions = $state([]);
  let transitioning = $state(false);
  let isWatching = $state(false);
  let watchBusy = $state(false);
  let personalTaskCount = $state(0);
  let startingTimer = $state(false);
  let children = $state([]);
  let ancestors = $state([]);
  // Bumped on every itemId change so in-flight loads for a previous item can't
  // write stale state when the user navigates item → item (e.g. tapping a
  // sub-item) without the component remounting.
  let loadToken = 0;

  // SCM (commits/PRs) + coding-agent panels — gated exactly like the desktop
  // ItemDetail: SCM via the per-item connection-status probe (has_repositories),
  // agent via a one-row agent-runs probe. Bodies mount lazily on expand.
  let scmAvailable = $state(false);
  let hasAgentRuns = $state(false);
  let scmOpen = $state(false);
  let agentOpen = $state(false);

  const descriptionHtml = $derived(item?.description ? renderMarkdown(item.description) : '');
  const itemKey = $derived(formatItemKey(item) ?? '');
  const projectId = $derived(item?.time_project_id ?? item?.effective_project_id ?? null);
  const canStartTimer = $derived(!!item && !timerStore.hasActive && !!projectId);

  function normalizeChild(c) {
    return {
      itemId: c.id,
      itemKey: formatItemKey(c),
      title: c.title,
      statusName: c.status_name,
      statusColor: c.status_color,
      priorityName: c.priority_name,
      priorityColor: c.priority_color,
      dueDate: c.due_date ? new Date(c.due_date) : null,
    };
  }

  async function loadItem(id, token) {
    loading = true;
    errored = false;
    try {
      const loaded = await api.items.get(id);
      if (token !== loadToken) return;
      item = loaded;
    } catch (err) {
      if (token !== loadToken) return;
      console.error('Failed to load item:', err);
      errored = true;
    } finally {
      if (token === loadToken) loading = false;
    }
  }

  async function loadAux(id, token) {
    // Best-effort side data — failures here shouldn't blank the screen. Every
    // assignment is guarded by the load token so a stale item's response can't
    // overwrite the current one.
    try {
      const res = await api.items.getAvailableStatusTransitions(id);
      if (token === loadToken) transitions = res?.available_transitions ?? [];
    } catch (err) {
      console.error('Failed to load transitions:', err);
    }
    try {
      const res = await api.items.getWatchStatus(id);
      if (token === loadToken) isWatching = res?.watching || false;
    } catch (err) {
      console.error('Failed to load watch status:', err);
    }
    try {
      const tasks = await api.items.getPersonalTasks(id);
      if (token === loadToken) personalTaskCount = Array.isArray(tasks) ? tasks.length : (tasks?.items?.length ?? 0);
    } catch {
      if (token === loadToken) personalTaskCount = 0;
    }
    try {
      const res = await api.items.getChildren(id);
      const list = Array.isArray(res) ? res : (res?.items ?? []);
      if (token === loadToken) children = list.filter((c) => c?.id).map(normalizeChild);
    } catch {
      if (token === loadToken) children = [];
    }
    // Parent chain (root → immediate parent) for the up-navigation breadcrumb.
    if (item?.parent_id) {
      try {
        const anc = await api.items.getAncestors(id);
        const list = Array.isArray(anc) ? anc : (anc?.items ?? []);
        if (token === loadToken) ancestors = list.filter((a) => a?.id);
      } catch {
        if (token === loadToken) ancestors = [];
      }
    }
    // SCM gate: show the panel unless the connection probe says the workspace
    // has no repositories (matches ItemSCMLinks' own has_repositories gate;
    // personal workspaces have no repos, so this also covers the desktop's
    // non-personal check).
    try {
      const cs = await api.itemSCMLinks.getConnectionStatus(id);
      if (token === loadToken) scmAvailable = cs?.has_repositories !== false;
    } catch {
      if (token === loadToken) scmAvailable = false;
    }
    // Agent gate: a coding-agent session exists iff a one-row probe is non-empty.
    try {
      const runs = await agentRuns.listForItem(id, { limit: 1 });
      if (token === loadToken) hasAgentRuns = (runs?.length ?? 0) > 0;
    } catch {
      if (token === loadToken) hasAgentRuns = false;
    }
  }

  async function changeStatus(statusId) {
    if (transitioning) return;
    transitioning = true;
    try {
      const updated = await api.items.transition(itemId, statusId);
      item = { ...item, ...updated };
      await loadAux(itemId, loadToken);
    } catch (err) {
      console.error('Failed to transition item:', err);
    } finally {
      transitioning = false;
    }
  }

  async function updateAssignee(user) {
    const assigneeId = user?.id ?? null;
    if (assigneeId === (item.assignee_id ?? null)) return;
    try {
      const updated = await api.items.update(itemId, { assignee_id: assigneeId });
      const name = user
        ? `${user.first_name || ''} ${user.last_name || ''}`.trim() || user.email || user.username || ''
        : null;
      item = { ...item, ...updated, assignee_id: assigneeId, assignee_name: name, assignee_avatar: user?.avatar_url ?? null };
    } catch (err) {
      console.error('Failed to update assignee:', err);
    }
  }

  async function toggleWatch() {
    if (watchBusy) return;
    watchBusy = true;
    try {
      if (isWatching) {
        await api.items.removeWatch(itemId);
        isWatching = false;
      } else {
        await api.items.addWatch(itemId);
        isWatching = true;
      }
    } catch (err) {
      console.error('Failed to toggle watch:', err);
    } finally {
      watchBusy = false;
    }
  }

  async function startTimer() {
    if (!canStartTimer || startingTimer) return;
    startingTimer = true;
    try {
      await timerStore.start({
        workspace_id: item.workspace_id,
        item_id: item.id,
        project_id: projectId,
        description: `Working on ${item.title}`,
      });
      navigate('/m/timer');
    } catch (err) {
      console.error('Failed to start timer:', err);
    } finally {
      startingTimer = false;
    }
  }

  // Reload whenever the item id changes — the component is not remounted when
  // navigating item → item (e.g. tapping a sub-item), so onMount wouldn't fire.
  $effect(() => {
    const id = itemId;
    if (id == null) return;
    const token = ++loadToken;
    // Reset transient state for the incoming item.
    item = null;
    transitions = [];
    isWatching = false;
    personalTaskCount = 0;
    children = [];
    ancestors = [];
    scmAvailable = false;
    hasAgentRuns = false;
    scmOpen = false;
    agentOpen = false;
    loadItem(id, token).then(() => {
      if (token === loadToken && !errored) loadAux(id, token);
    });
  });
</script>

<MobileHeader title={itemKey} onback={() => (window.history.length > 1 ? window.history.back() : navigate('/m'))} />

{#if loading}
  <div class="center" data-testid="detail-loading"><Loader class="spin" size={22} /></div>
{:else if errored || !item}
  <p class="msg" data-testid="detail-error">Couldn't load this item.</p>
{:else}
  <div class="detail" data-testid="mobile-item-detail">
    {#if ancestors.length > 0}
      <nav class="breadcrumb" data-testid="detail-breadcrumb" aria-label="Parent items">
        {#each ancestors as anc, i (anc.id)}
          {#if i > 0}<ChevronRight size={13} class="bc-sep" />{/if}
          <button class="bc-link" onclick={() => navigate(`/m/items/${anc.id}`)} data-testid="breadcrumb-link" type="button">
            {formatItemKey(anc) ?? anc.title}
          </button>
        {/each}
      </nav>
    {/if}

    {#if item.item_type_name}
      <div class="status-line"><span class="type">{item.item_type_name}</span></div>
    {/if}

    <h1 class="title" data-testid="detail-title">{item.title}</h1>

    <!-- Status + assignee pickers. Status options come from the workflow's
         available-transitions endpoint (not hardcoded), so custom workflows
         and custom statuses work as-is. Assignee users come from the
         workspace-scoped assignable-users endpoint via UserPicker. -->
    <div class="fields">
      <BasePicker
        value={item.status_id ?? null}
        items={transitions}
        getValue={(s) => s.id}
        getLabel={(s) => s.name}
        disabled={transitioning || transitions.length === 0}
        allowClear={false}
        positioning={{ strategy: 'fixed', placement: 'bottom-start', sameWidth: true }}
        onSelect={(s) => s && changeStatus(s.id)}
      >
        {#snippet children()}
          <div class="field" data-testid="status-picker-trigger">
            <span class="field-label">Status</span>
            <span class="field-value" data-testid="detail-status">
              <StatusPill name={item.status_name} color={item.status_color} />
              <ChevronDown size={16} class="chev" />
            </span>
          </div>
        {/snippet}
        {#snippet itemSnippet({ item: opt })}
          <span class="opt">
            <span class="opt-dot" style={opt.category_color ? `background-color: ${opt.category_color};` : ''}></span>
            {opt.name}
          </span>
        {/snippet}
      </BasePicker>

      <UserPicker
        value={item.assignee_id ?? null}
        workspaceId={item.workspace_id}
        showUnassigned={true}
        positioning={{ strategy: 'fixed', placement: 'bottom-start', sameWidth: true }}
        onSelect={updateAssignee}
      >
        {#snippet children()}
          <div class="field" data-testid="assignee-picker-trigger">
            <span class="field-label">Assignee</span>
            <span class="field-value">
              {#if item.assignee_id && item.assignee_name}
                <Avatar src={item.assignee_avatar} name={item.assignee_name} size="xs" variant="teal" />
                <span class="assignee-name">{item.assignee_name}</span>
              {:else}
                <span class="muted">Unassigned</span>
              {/if}
              <ChevronDown size={16} class="chev" />
            </span>
          </div>
        {/snippet}
      </UserPicker>
    </div>

    {#if descriptionHtml}
      <div class="html-content desc" data-testid="detail-description">{@html descriptionHtml}</div>
    {/if}

    <!-- Meta -->
    {#if item.due_date || personalTaskCount > 0}
      <dl class="meta">
        {#if item.due_date}
          <div><dt>Due</dt><dd>{formatDate(item.due_date)}</dd></div>
        {/if}
        {#if personalTaskCount > 0}
          <div><dt>Personal tasks</dt><dd>{personalTaskCount} linked</dd></div>
        {/if}
      </dl>
    {/if}

    <!-- Actions -->
    <div class="actions">
      <button class="act" class:on={isWatching} onclick={toggleWatch} disabled={watchBusy} data-testid="detail-watch" type="button">
        <Star size={16} fill={isWatching ? 'currentColor' : 'none'} />
        {isWatching ? 'Watching' : 'Watch'}
      </button>
      {#if canStartTimer}
        <button class="act" onclick={startTimer} disabled={startingTimer} data-testid="detail-start-timer" type="button">
          <Play size={16} /> Start timer
        </button>
      {/if}
    </div>

    <!-- Sub-items -->
    {#if children.length > 0}
      <section class="subitems" data-testid="detail-subitems">
        <h2 class="section-title">Sub-items <span class="count">{children.length}</span></h2>
        <div class="subitems-list">
          {#each children as child (child.itemId)}
            <MobileItemRow {...child} />
          {/each}
        </div>
      </section>
    {/if}

    <!-- Commits & pull requests (collapsible; mounts on open) -->
    {#if scmAvailable}
      <section class="panel" data-testid="scm-panel">
        <button class="panel-head" onclick={() => (scmOpen = !scmOpen)} aria-expanded={scmOpen} data-testid="scm-panel-toggle" type="button">
          <span class="panel-title"><GitPullRequest size={16} /> Commits &amp; pull requests</span>
          <ChevronDown size={18} class={scmOpen ? 'chev open' : 'chev'} />
        </button>
        {#if scmOpen}
          <div class="panel-body" data-testid="scm-panel-body">
            <!-- Read-only on mobile: SCM write actions stay on desktop, so the
                 create handlers are no-ops (also satisfies required props). -->
            <ItemSCMLinks itemId={item.id} onaddlink={() => {}} oncreatebranch={() => {}} oncreatepr={() => {}} />
          </div>
        {/if}
      </section>
    {/if}

    <!-- Coding agent (collapsible; only shown when a session exists) -->
    {#if hasAgentRuns}
      <section class="panel" data-testid="agent-panel">
        <button class="panel-head" onclick={() => (agentOpen = !agentOpen)} aria-expanded={agentOpen} data-testid="agent-panel-toggle" type="button">
          <span class="panel-title"><Bot size={16} /> Coding agent</span>
          <ChevronDown size={18} class={agentOpen ? 'chev open' : 'chev'} />
        </button>
        {#if agentOpen}
          <div class="panel-body" data-testid="agent-panel-body">
            <ItemAgentLog itemId={item.id} workspaceId={item.workspace_id} />
          </div>
        {/if}
      </section>
    {/if}

    <!-- Comments. Keyed on itemId so it reloads when navigating item → item. -->
    <section class="comments">
      {#key itemId}
        <Comments {itemId} onCommentsLoaded={() => {}} />
      {/key}
    </section>
  </div>
{/if}

<style>
  .center { display: flex; justify-content: center; padding: 3rem; color: var(--ds-text-subtle); }
  :global(.spin) { animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }

  .msg { padding: 3rem 1.25rem; text-align: center; color: var(--ds-text-subtle); }

  .detail { padding: 0.75rem 0.875rem 2rem; }

  .breadcrumb {
    display: flex; align-items: center; flex-wrap: nowrap; gap: 0.15rem;
    margin-bottom: 0.6rem; overflow-x: auto; -webkit-overflow-scrolling: touch;
  }
  .bc-link {
    flex-shrink: 0; border: none; background: transparent; cursor: pointer;
    padding: 2px 4px; font-family: var(--font-mono, monospace); font-size: 0.75rem;
    color: var(--ds-text-link, var(--ds-interactive)); white-space: nowrap;
  }
  .breadcrumb :global(.bc-sep) { flex-shrink: 0; color: var(--ds-text-subtlest, var(--ds-text-subtle)); }

  .status-line { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; }
  .type { font-size: 0.75rem; color: var(--ds-text-subtle); text-transform: uppercase; letter-spacing: 0.02em; }

  .title { font-size: 1.25rem; font-weight: var(--font-semibold, 600); color: var(--ds-text); margin: 0 0 1rem; line-height: 1.3; }

  /* Status + assignee picker field rows */
  .fields {
    margin-bottom: 1rem; border: 1px solid var(--ds-border); border-radius: var(--radius-lg, 8px); overflow: hidden;
  }
  .field {
    display: flex; align-items: center; justify-content: space-between; gap: 1rem;
    min-height: 48px; padding: 0.5rem 0.85rem; cursor: pointer;
  }
  .field:not(:last-child) { border-bottom: 1px solid var(--ds-border); }
  .field:active { background-color: var(--ds-background-neutral-hovered); }
  .field-label { font-size: 0.8125rem; color: var(--ds-text-subtle); }
  .field-value { display: inline-flex; align-items: center; gap: 0.5rem; min-width: 0; color: var(--ds-text); font-size: 0.875rem; }
  .field-value :global(.chev) { color: var(--ds-icon-subtle, var(--ds-text-subtle)); flex-shrink: 0; }
  .assignee-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 9rem; }
  .muted { color: var(--ds-text-subtle); }
  .opt { display: inline-flex; align-items: center; gap: 0.5rem; }
  .opt-dot { width: 8px; height: 8px; border-radius: var(--radius-full, 9999px); background-color: var(--ds-icon-subtle, var(--ds-text-subtle)); flex-shrink: 0; }

  .desc {
    padding: 0.5rem 0 1rem; border-bottom: 1px solid var(--ds-border); margin-bottom: 1rem;
    color: var(--ds-text); font-size: 0.9375rem; line-height: 1.55; overflow-wrap: anywhere;
  }

  .meta { margin: 0 0 1rem; display: flex; flex-direction: column; gap: 0.6rem; }
  .meta div { display: flex; justify-content: space-between; gap: 1rem; }
  .meta dt { color: var(--ds-text-subtle); font-size: 0.8125rem; margin: 0; }
  .meta dd { color: var(--ds-text); font-size: 0.875rem; margin: 0; text-align: right; }

  .actions { display: flex; gap: 0.5rem; margin-bottom: 1.5rem; }
  .act {
    flex: 1; display: inline-flex; align-items: center; justify-content: center; gap: 0.4rem;
    min-height: 44px; border: 1px solid var(--ds-border); border-radius: var(--radius-lg, 8px);
    background-color: var(--ds-surface); color: var(--ds-text); font-size: 0.875rem; font-weight: var(--font-medium, 500); cursor: pointer;
  }
  .act.on { color: var(--ds-interactive); border-color: var(--ds-interactive); }
  .act:disabled { opacity: 0.6; }

  .panel { border: 1px solid var(--ds-border); border-radius: var(--radius-lg, 8px); margin-bottom: 0.75rem; overflow: hidden; }
  .panel-head {
    width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 0.5rem;
    min-height: 48px; padding: 0 0.85rem; background-color: var(--ds-surface); color: var(--ds-text);
    border: none; cursor: pointer; font-size: 0.9375rem; font-weight: var(--font-medium, 500);
  }
  .panel-title { display: inline-flex; align-items: center; gap: 0.5rem; }
  :global(.chev) { transition: transform var(--duration-fast, 100ms) ease; color: var(--ds-icon-subtle, var(--ds-text-subtle)); }
  :global(.chev.open) { transform: rotate(180deg); }
  .panel-body { padding: 0.5rem 0.85rem 0.85rem; border-top: 1px solid var(--ds-border); overflow-x: auto; }

  .subitems { margin-bottom: 1rem; }
  .section-title {
    display: flex; align-items: center; gap: 0.5rem;
    font-size: 0.9375rem; font-weight: var(--font-semibold, 600); color: var(--ds-text); margin: 0 0 0.5rem;
  }
  .section-title .count {
    font-size: 0.75rem; font-weight: var(--font-medium, 500); color: var(--ds-text-subtle);
    background-color: var(--ds-background-neutral); border-radius: var(--radius-full, 9999px); padding: 0 0.45rem;
  }
  .subitems-list {
    border: 1px solid var(--ds-border); border-radius: var(--radius-lg, 8px); overflow: hidden;
  }
  /* MobileItemRow draws its own bottom border; drop it on the last row so it
     doesn't double the container border. */
  .subitems-list :global([data-testid='mobile-item-row']:last-child) { border-bottom: none; }

  .comments { border-top: 1px solid var(--ds-border); padding-top: 1rem; }
</style>
