<script>
  import { MessageSquare, Clock, Play, Info, History, Edit, Trash2, MoreHorizontal, Bot, Activity, CalendarClock, TimerReset } from '@lucide/svelte';
  import Button from '../../components/Button.svelte';
  import DropdownMenu from '../../layout/DropdownMenu.svelte';
  import Comments from '../items/Comments.svelte';
  import ItemHistory from '../items/ItemHistory.svelte';
  import ItemAgentLog from '../items/ItemAgentLog.svelte';
  import { agentRuns } from '../../api/agentRuns.js';
  import { confirm } from '../../composables/useConfirm.js';
  import { dateOnlyKey, formatDateOnly, formatDueDate, formatStatusAge, getDaysOverdue, worklogDateKey } from '../../utils/dateFormatter.js';
  import { serverNow } from '../../utils/serverClock.js';
  import { formatAuthenticatedDateTime as formatDateTimeLocale, formatAuthenticatedInstant } from '../../utils/authenticatedDateFormatter.js';
  import { t } from '../../stores/i18n.svelte.js';
  import { durationToString } from '../../utils/timeUtils.js';
  import { toHotkeyString, getShortcutDisplay } from '../../utils/keyboardShortcuts.js';
  import Badge from '../../components/Badge.svelte';
  import DescriptionText from '../../components/DescriptionText.svelte';
  import EmptyState from '../../components/EmptyState.svelte';
  import DataTable from '../../components/DataTable.svelte';
  import Toggle from '../../components/Toggle.svelte';
  // Direct store access for the child-item rollup keeps the time-tab logic
  // co-located and avoids threading four extra props through ItemDetail and
  // ItemDetailContent for a feature that only lives in this tab.
  import { itemDetailStore, workspaceDataStore } from '../../stores';

  let {
    item,
    workspace,
    tab = 'comments',
    moduleSettings = { time_tracking_enabled: true },
    timeWorklogs = [],
    activeTimer = null,
    statusOptions = [],
    onswitchtab = undefined,
    onstarttimer = undefined,
    onlogtime = undefined,
    oneditworklog = undefined,
    ondeleteworklog = undefined,
  } = $props();

  function getStatusName(statusId) {
    if (!statusId) return '';
    const status = statusOptions.find(s => s.id === statusId);
    return status?.name || '';
  }

  let commentCount = $state(0);

  // Agent log tab (WI-260): only rendered when the item has at least one
  // agent run — one cheap limit=1 probe per item; a 404/permission failure
  // simply hides the tab.
  let hasAgentRuns = $state(false);
  $effect(() => {
    const id = item?.id;
    hasAgentRuns = false;
    if (!id) return;
    agentRuns
      .listForItem(id, { limit: 1 })
      .then((runs) => { if (item?.id === id) hasAgentRuns = (runs?.length ?? 0) > 0; })
      .catch(() => {});
  });

  // Sum logged worklog minutes for the time tab header. When the
  // "Include child items" toggle is on, swap to the server-side rollup totals
  // (covers root + descendants) which itemDetailStore caches.
  const includeChildItems = $derived(itemDetailStore.includeChildItems);
  const rollup = $derived(itemDetailStore.timeRollup);
  const rollupLoading = $derived(itemDetailStore.timeRollupLoading);

  const totalLoggedMinutes = $derived(
    includeChildItems && rollup
      ? Number(rollup.total_logged_minutes) || 0
      : (timeWorklogs ?? []).reduce(
          (sum, w) => sum + (Number(w?.duration_minutes) || 0),
          0,
        ),
  );
  const estimateMinutes = $derived.by(() => {
    if (includeChildItems && rollup) {
      const v = Number(rollup.total_estimate_minutes) || 0;
      return v > 0 ? v : 0;
    }
    return Number.isFinite(item?.estimate_minutes) && item?.estimate_minutes > 0
      ? item.estimate_minutes
      : 0;
  });
  const hasEstimate = $derived(estimateMinutes > 0);
  const loggedRatio = $derived(
    hasEstimate ? totalLoggedMinutes / estimateMinutes : 0,
  );
  const overBudget = $derived(hasEstimate && totalLoggedMinutes > estimateMinutes);

  const STALE_AFTER_DAYS = 14;
  const DUE_SOON_DAYS = 7;

  function getElapsedDays(value) {
    if (!value) return null;
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return null;
    return Math.max(0, Math.floor((serverNow().getTime() - date.getTime()) / 86400000));
  }

  function isItemCompleted(currentItem) {
    if (currentItem?.completed_at) return true;
    return workspaceDataStore.statuses.some(
      (status) => Number(status?.id) === Number(currentItem?.status_id) && status?.is_completed,
    );
  }

  function getActivityHealth(currentItem) {
    if (isItemCompleted(currentItem)) {
      return { state: 'completed', days: null, variant: 'success' };
    }

    const days = getElapsedDays(currentItem?.last_active_at || currentItem?.updated_at);
    if (days === null) {
      return { state: 'unknown', days: null, variant: 'neutral' };
    }
    if (days >= STALE_AFTER_DAYS) {
      return { state: 'stale', days, variant: 'warning' };
    }
    return { state: 'active', days, variant: 'success' };
  }

  function getDueHealth(currentItem) {
    if (isItemCompleted(currentItem)) {
      return { state: 'completed', days: null, variant: 'success' };
    }
    if (!dateOnlyKey(currentItem?.due_date)) {
      return { state: 'unscheduled', days: null, variant: 'neutral' };
    }

    const days = -getDaysOverdue(currentItem.due_date);
    if (days < 0) return { state: 'overdue', days, variant: 'danger' };
    if (days === 0) return { state: 'today', days, variant: 'warning' };
    if (days <= DUE_SOON_DAYS) return { state: 'soon', days, variant: 'warning' };
    return { state: 'scheduled', days, variant: 'info' };
  }

  const activityHealth = $derived(getActivityHealth(item));
  const dueHealth = $derived(getDueHealth(item));
  const statusAge = $derived(formatStatusAge(item?.status_since));

  function getActivityLabel(state) {
    return t(`items.activityHealth${state.charAt(0).toUpperCase()}${state.slice(1)}`);
  }

  function getDueLabel(state) {
    return t(`items.dueHealth${state.charAt(0).toUpperCase()}${state.slice(1)}`);
  }

  function getItemKey() {
    const workspaceKey = workspace?.key || item?.workspace_key || 'WORK';
    return `${workspaceKey}-${item?.workspace_item_number ?? item?.id}`;
  }

  function getParentKey() {
    const workspaceKey = workspace?.key || item?.workspace_key || 'WORK';
    return `${workspaceKey}-${item?.parent_workspace_item_number ?? item?.parent_id}`;
  }

  function handleToggleChildItems(checked) {
    itemDetailStore.includeChildItems = checked;
    if (checked) {
      itemDetailStore.loadTimeRollup();
    }
  }

  function switchTab(newTab) {
    onswitchtab?.({ tab: newTab });
  }
  
  function getDefaultProjectForTimeLogging() {
    // Priority order for project resolution:
    // 1. Item-specific time tracking project override
    if (item?.time_project_id) {
      return item.time_project_id;
    }
    // 2. Effective project (inherited or direct project_id)
    if (item?.effective_project_id) {
      return item.effective_project_id;
    }
    // 3. Workspace default time tracking project
    if (workspace?.time_project_id) {
      return workspace.time_project_id;
    }
    return null;
  }
  
  function handleStartTimer() {
    onstarttimer?.();
  }

  function handleLogTime() {
    onlogtime?.();
  }
  
  function handleCommentsLoaded(data) {
    commentCount = data.count;
  }

  function handleEditWorklog(worklog) {
    oneditworklog?.(worklog);
  }

  async function handleDeleteWorklog(worklog) {
    const ok = await confirm({
      title: t('items.deleteTimeEntry'),
      message: t('items.deleteTimeEntryConfirm'),
      confirmText: t('common.delete'),
      variant: 'danger',
    });
    if (!ok) return;
    ondeleteworklog?.(worklog);
  }

  function buildWorklogDropdownItems(worklog) {
    return [
      {
        id: 'edit',
        type: 'regular',
        icon: Edit,
        title: t('common.edit'),
        onClick: () => handleEditWorklog(worklog)
      },
      {
        id: 'delete',
        type: 'regular',
        icon: Trash2,
        title: t('common.delete'),
        color: 'var(--ds-text-danger)',
        onClick: () => handleDeleteWorklog(worklog)
      }
    ];
  }

  const worklogColumns = [
    { key: 'date', label: t('common.date'), render: (w) => worklogDateKey(w.date), textColor: 'var(--ds-text-subtle)' },
    { key: 'description', label: t('common.description'), render: (w) => w.description || t('items.noDescription') },
    { key: 'user_name', label: t('common.user'), render: (w) => w.user_name || '—' },
    { key: 'start_time', label: t('time.start'), render: (w) => w.start_time ? formatAuthenticatedInstant(w.start_time * 1000, { hour: '2-digit', minute: '2-digit' }) : '—', textColor: 'var(--ds-text-subtle)' },
    { key: 'end_time', label: t('time.end'), render: (w) => w.end_time ? formatAuthenticatedInstant(w.end_time * 1000, { hour: '2-digit', minute: '2-digit' }) : '—', textColor: 'var(--ds-text-subtle)' },
    { key: 'duration_minutes', label: t('time.duration'), render: (w) => `${Math.floor(w.duration_minutes / 60)}h ${w.duration_minutes % 60}m` },
    { key: 'project_name', label: t('common.project'), textColor: 'var(--ds-text-subtle)' },
    { key: 'actions', label: '', width: 'w-12' },
  ];
</script>

<div class="mt-6">
  <div>
    <!-- Tab Navigation -->
    <div class="flex border-b" style="border-color: var(--ds-border);">
      <button
        class="flex items-center gap-2 pl-0 pr-4 py-3 text-sm font-medium transition-all relative"
        style="{tab === 'comments' ? 'background-color: var(--ds-surface-raised); color: var(--ds-interactive); margin-bottom: -1px; border-bottom: 2px solid var(--ds-interactive);' : 'color: var(--ds-text-subtle);'}"
        onclick={() => switchTab('comments')}
      >
        <MessageSquare class="w-4 h-4" />
        {t('items.comments')}
        {#if commentCount > 0}
          <Badge variant="neutral" size="xs">{commentCount}</Badge>
        {/if}
      </button>
      {#if moduleSettings.time_tracking_enabled}
        <button
          data-testid="item-detail-time-tab"
          class="flex items-center gap-2 px-4 py-3 text-sm font-medium transition-all relative"
          style="{tab === 'time' ? 'background-color: var(--ds-surface-raised); color: var(--ds-interactive); margin-bottom: -1px; border-bottom: 2px solid var(--ds-interactive);' : 'color: var(--ds-text-subtle);'}"
          onclick={() => switchTab('time')}
        >
          <Clock class="w-4 h-4" />
          {t('items.timeTracking')}
          {#if timeWorklogs && timeWorklogs.length > 0}
            <Badge variant="neutral" size="xs">{timeWorklogs.length}</Badge>
          {/if}
        </button>
      {/if}
      <button
        class="flex items-center gap-2 px-4 py-3 text-sm font-medium transition-all relative"
        style="{tab === 'details' ? 'background-color: var(--ds-surface-raised); color: var(--ds-interactive); margin-bottom: -1px; border-bottom: 2px solid var(--ds-interactive);' : 'color: var(--ds-text-subtle);'}"
        onclick={() => switchTab('details')}
      >
        <Info class="w-4 h-4" />
        {t('items.details')}
      </button>
      <button
        data-testid="item-detail-history-tab"
        class="flex items-center gap-2 px-4 py-3 text-sm font-medium transition-all relative"
        style="{tab === 'history' ? 'background-color: var(--ds-surface-raised); color: var(--ds-interactive); margin-bottom: -1px; border-bottom: 2px solid var(--ds-interactive);' : 'color: var(--ds-text-subtle);'}"
        onclick={() => switchTab('history')}
      >
        <History class="w-4 h-4" />
        {t('items.history')}
      </button>
      {#if hasAgentRuns}
        <button
          data-testid="item-detail-agent-log-tab"
          class="flex items-center gap-2 px-4 py-3 text-sm font-medium transition-all relative"
          style="{tab === 'agent-log' ? 'background-color: var(--ds-surface-raised); color: var(--ds-interactive); margin-bottom: -1px; border-bottom: 2px solid var(--ds-interactive);' : 'color: var(--ds-text-subtle);'}"
          onclick={() => switchTab('agent-log')}
        >
          <Bot class="w-4 h-4" />
          {t('items.agentLog')}
        </button>
      {/if}
    </div>

    <!-- Tab Content -->
    <div class="pt-6">
      {#if tab === 'comments'}
        <Comments itemId={item.id} isPersonalWorkspace={workspace?.is_personal} isPortalRequest={!!item.request_type_id} enableInternalComments={workspace?.internal_comments_enabled} onCommentsLoaded={handleCommentsLoaded} />
      {:else if tab === 'details'}
        <div class="grid gap-8" data-testid="item-details-overview">
          <section class="overflow-hidden rounded-xl border border-[var(--ds-border)] bg-[var(--ds-surface-raised)]" aria-labelledby="item-health-heading">
            <div class="border-b border-[var(--ds-border)] px-4 py-4 min-[421px]:px-6 min-[421px]:pt-5">
              <h3 id="item-health-heading" class="text-sm font-semibold text-[var(--ds-text)]">{t('items.healthOverview')}</h3>
              <p class="mt-1 max-w-[65ch] text-xs leading-5 text-[var(--ds-text-subtle)]">{t('items.healthOverviewDescription')}</p>
            </div>

            <div class="grid min-[761px]:grid-cols-3">
              <div class="min-w-0 px-4 py-5 min-[421px]:px-6" data-testid="item-health-activity">
                <div class="flex items-center justify-between gap-3">
                  <span class="inline-flex items-center gap-2 text-xs font-semibold text-[var(--ds-text-subtle)]"><Activity class="h-4 w-4" />{t('items.activity')}</span>
                  <Badge variant={activityHealth.variant} size="xs">{getActivityLabel(activityHealth.state)}</Badge>
                </div>
                {#if activityHealth.state === 'completed'}
                  <p class="mt-4 text-base font-semibold leading-snug text-[var(--ds-text)]">{t('items.activityMonitoringComplete')}</p>
                {:else if activityHealth.days === 0}
                  <p class="mt-4 text-base font-semibold leading-snug text-[var(--ds-text)]">{t('items.activityToday')}</p>
                {:else if activityHealth.days !== null}
                  <p class="mt-4 text-base font-semibold leading-snug text-[var(--ds-text)]">{t('items.activityIdleDays', { count: activityHealth.days })}</p>
                {:else}
                  <p class="mt-4 text-base font-semibold leading-snug text-[var(--ds-text)]">—</p>
                {/if}
                <p class="mt-1 text-xs leading-5 text-[var(--ds-text-subtle)]">
                  {#if item.last_active_at || item.updated_at}
                    {t('items.lastActivityAt', { date: formatDateTimeLocale(item.last_active_at || item.updated_at) })}
                  {:else}
                    {t('items.activityUnavailable')}
                  {/if}
                </p>
              </div>

              <div class="min-w-0 border-t border-[var(--ds-border)] px-4 py-5 min-[421px]:px-6 min-[761px]:border-t-0 min-[761px]:border-l" data-testid="item-health-due-date">
                <div class="flex items-center justify-between gap-3">
                  <span class="inline-flex items-center gap-2 text-xs font-semibold text-[var(--ds-text-subtle)]"><CalendarClock class="h-4 w-4" />{t('items.dueDate')}</span>
                  <Badge variant={dueHealth.variant} size="xs">{getDueLabel(dueHealth.state)}</Badge>
                </div>
                {#if dueHealth.state === 'completed'}
                  <p class="mt-4 text-base font-semibold leading-snug text-[var(--ds-text)]">
                    {item.completed_at ? t('items.completedOn', { date: formatDateOnly(item.completed_at) }) : t('items.workCompleted')}
                  </p>
                {:else}
                  <p class="mt-4 text-base font-semibold leading-snug text-[var(--ds-text)]">{item.due_date ? formatDueDate(item.due_date) : t('dueDate.noDueDate')}</p>
                {/if}
                <p class="mt-1 text-xs leading-5 text-[var(--ds-text-subtle)]">
                  {#if item.due_date}
                    {t('items.dueOn', { date: formatDateOnly(item.due_date) })}
                  {:else}
                    {t('items.dueDateUnavailable')}
                  {/if}
                </p>
              </div>

              <div class="min-w-0 border-t border-[var(--ds-border)] px-4 py-5 min-[421px]:px-6 min-[761px]:border-t-0 min-[761px]:border-l" data-testid="item-health-status-age">
                <div class="flex items-center justify-between gap-3">
                  <span class="inline-flex items-center gap-2 text-xs font-semibold text-[var(--ds-text-subtle)]"><TimerReset class="h-4 w-4" />{t('items.timeInStatus')}</span>
                  <Badge variant="neutral" size="xs">{item.status_name || getStatusName(item.status_id) || t('items.unknown')}</Badge>
                </div>
                <p class="mt-4 text-base font-semibold leading-snug text-[var(--ds-text)]">{statusAge || '—'}</p>
                <p class="mt-1 text-xs leading-5 text-[var(--ds-text-subtle)]">
                  {#if item.status_since}
                    {t('items.statusSince', { date: formatDateTimeLocale(item.status_since) })}
                  {:else}
                    {t('items.statusHistoryUnavailable')}
                  {/if}
                </p>
              </div>
            </div>
          </section>

          <div class="grid gap-8 min-[761px]:grid-cols-2 min-[761px]:gap-10">
            <section aria-labelledby="item-timeline-heading">
              <h3 id="item-timeline-heading" class="mb-3 text-sm font-semibold text-[var(--ds-text)]">{t('items.timeline')}</h3>
              <dl class="border-t border-[var(--ds-border)]">
                <div class="grid gap-1 border-b border-[var(--ds-border)] py-3.5 min-[421px]:grid-cols-[minmax(7rem,0.8fr)_minmax(0,1.2fr)] min-[421px]:gap-4">
                  <dt class="text-xs text-[var(--ds-text-subtle)]">{t('items.created')}</dt>
                  <dd class="min-w-0 [overflow-wrap:anywhere] text-left text-xs text-[var(--ds-text)] min-[421px]:text-right">
                    <span class="block text-sm">{formatDateTimeLocale(item.created_at) || '—'}</span>
                    {#if item.creator_name}
                      <DescriptionText>{t('items.by')} {item.creator_name}</DescriptionText>
                    {/if}
                  </dd>
                </div>
                <div class="grid gap-1 border-b border-[var(--ds-border)] py-3.5 min-[421px]:grid-cols-[minmax(7rem,0.8fr)_minmax(0,1.2fr)] min-[421px]:gap-4">
                  <dt class="text-xs text-[var(--ds-text-subtle)]">{t('items.lastUpdated')}</dt>
                  <dd class="min-w-0 [overflow-wrap:anywhere] text-left text-xs text-[var(--ds-text)] min-[421px]:text-right">
                    <span class="block text-sm">{formatDateTimeLocale(item.updated_at) || '—'}</span>
                    {#if item.updated_by_name}
                      <DescriptionText>{t('items.by')} {item.updated_by_name}</DescriptionText>
                    {/if}
                  </dd>
                </div>
              </dl>
            </section>

            <section aria-labelledby="item-information-heading">
              <h3 id="item-information-heading" class="mb-3 text-sm font-semibold text-[var(--ds-text)]">{t('items.workItemInformation')}</h3>
              <dl class="border-t border-[var(--ds-border)]">
                <div class="grid gap-1 border-b border-[var(--ds-border)] py-3.5 min-[421px]:grid-cols-[minmax(7rem,0.8fr)_minmax(0,1.2fr)] min-[421px]:gap-4">
                  <dt class="text-xs text-[var(--ds-text-subtle)]">{t('items.id')}</dt>
                  <dd class="min-w-0 [overflow-wrap:anywhere] text-left font-mono text-xs text-[var(--ds-text)] min-[421px]:text-right">{getItemKey()}</dd>
                </div>
                <div class="grid gap-1 border-b border-[var(--ds-border)] py-3.5 min-[421px]:grid-cols-[minmax(7rem,0.8fr)_minmax(0,1.2fr)] min-[421px]:gap-4">
                  <dt class="text-xs text-[var(--ds-text-subtle)]">{t('items.type')}</dt>
                  <dd class="min-w-0 [overflow-wrap:anywhere] text-left text-xs text-[var(--ds-text)] min-[421px]:text-right">{item.item_type_name || t('items.workItem')}</dd>
                </div>
                {#if item.parent_id}
                  <div class="grid gap-1 border-b border-[var(--ds-border)] py-3.5 min-[421px]:grid-cols-[minmax(7rem,0.8fr)_minmax(0,1.2fr)] min-[421px]:gap-4">
                    <dt class="text-xs text-[var(--ds-text-subtle)]">{t('items.parent')}</dt>
                    <dd class="min-w-0 [overflow-wrap:anywhere] text-left font-mono text-xs text-[var(--ds-text)] min-[421px]:text-right">{getParentKey()}</dd>
                  </div>
                {/if}
              </dl>
            </section>
          </div>
        </div>
      {:else if tab === 'time' && moduleSettings.time_tracking_enabled}
        <!-- Time Entries List -->
        {#if timeWorklogs && timeWorklogs.length > 0}
          <div class="space-y-3">
            <div class="flex items-center justify-between">
              <div class="flex flex-col gap-1">
                <div class="flex items-center gap-3">
                  <h4 class="text-sm font-medium" style="color: var(--ds-text);">
                    {t('items.timeEntries')} ({timeWorklogs.length})
                  </h4>
                  <Toggle
                    size="small"
                    label={t('items.includeChildItems')}
                    checked={includeChildItems}
                    onchange={handleToggleChildItems}
                  />
                </div>
                <div class="text-xs" style="color: var(--ds-text-subtle);">
                  {#if rollupLoading}
                    {t('common.loading')}
                  {:else if hasEstimate}
                    <span style={overBudget ? 'color: var(--ds-text-danger, #cc3344); font-weight: 600;' : ''}>
                      {durationToString(totalLoggedMinutes, { withDays: true })}
                    </span>
                    {' '}{t('items.loggedOf')}{' '}
                    {durationToString(estimateMinutes, { withDays: true })}
                    {' '}{t('items.estimated')}
                  {:else}
                    {durationToString(totalLoggedMinutes, { withDays: true })} {t('items.logged')}
                  {/if}
                  {#if includeChildItems && rollup}
                    <span class="ml-1" style="color: var(--ds-text-subtle);">
                      ({t('items.rollupItemCount', { count: rollup.item_count })})
                    </span>
                  {/if}
                </div>
                {#if includeChildItems && rollup?.truncated}
                  <div class="text-xs" style="color: var(--ds-text-warning, #b45309);">
                    {t('items.rollupTruncated', { max: rollup.item_count })}
                  </div>
                {/if}
                {#if hasEstimate}
                  <div class="h-1 rounded overflow-hidden mt-0.5" style="background: var(--ds-background-neutral-subtle, var(--ds-surface-sunken)); width: 12rem;">
                    <div
                      class="h-full transition-all"
                      style="width: {Math.min(loggedRatio, 1) * 100}%; background: {overBudget ? 'var(--ds-text-danger, #cc3344)' : 'var(--ds-background-brand, #3b82f6)'};"
                    ></div>
                  </div>
                {/if}
              </div>
              <div class="flex gap-2">
                {#if !activeTimer && getDefaultProjectForTimeLogging()}
                  <Button
                    variant="primary"
                    icon={Play}
                    onclick={handleStartTimer}
                    size="small"
                    dataTestid="start-timer-btn"
                    title={t('items.startTimerTitle')}
                    keyboardHint={getShortcutDisplay('itemDetail', 'startTimer')}
                    hotkeyConfig={{ key: toHotkeyString('itemDetail', 'startTimer'), guard: () => tab === 'time' && moduleSettings?.time_tracking_enabled && !!getDefaultProjectForTimeLogging() }}
                  >
                    {t('items.startTimer')}
                  </Button>
                {/if}
                <Button
                  variant="default"
                  size="small"
                  onclick={handleLogTime}
                  title={t('items.logTimeTitle')}
                  keyboardHint={getShortcutDisplay('itemDetail', 'logTime')}
                  hotkeyConfig={{ key: toHotkeyString('itemDetail', 'logTime'), guard: () => tab === 'time' && moduleSettings?.time_tracking_enabled }}
                >
                  {t('items.logTime')}
                </Button>
              </div>
            </div>
            <DataTable
              columns={worklogColumns}
              data={timeWorklogs}
              keyField="id"
              actionItems={buildWorklogDropdownItems}
            />
          </div>
        {:else}
          <div class="mb-3 flex items-center">
            <Toggle
              size="small"
              label={t('items.includeChildItems')}
              checked={includeChildItems}
              onchange={handleToggleChildItems}
            />
          </div>
          {#if hasEstimate || (includeChildItems && rollup && totalLoggedMinutes > 0)}
            <div class="flex flex-col gap-1 mb-3">
              <div class="text-xs" style="color: var(--ds-text-subtle);">
                {#if rollupLoading}
                  {t('common.loading')}
                {:else if hasEstimate}
                  <span style={overBudget ? 'color: var(--ds-text-danger, #cc3344); font-weight: 600;' : ''}>
                    {durationToString(totalLoggedMinutes, { withDays: true })}
                  </span>
                  {' '}{t('items.loggedOf')}{' '}
                  {durationToString(estimateMinutes, { withDays: true })}
                  {' '}{t('items.estimated')}
                {:else}
                  {durationToString(totalLoggedMinutes, { withDays: true })} {t('items.logged')}
                {/if}
                {#if includeChildItems && rollup}
                  <span class="ml-1" style="color: var(--ds-text-subtle);">
                    ({t('items.rollupItemCount', { count: rollup.item_count })})
                  </span>
                {/if}
              </div>
              {#if includeChildItems && rollup?.truncated}
                <div class="text-xs" style="color: var(--ds-text-warning, #b45309);">
                  {t('items.rollupTruncated', { max: rollup.item_count })}
                </div>
              {/if}
              {#if hasEstimate}
                <div class="h-1 rounded overflow-hidden" style="background: var(--ds-background-neutral-subtle, var(--ds-surface-sunken)); width: 12rem;">
                  <div class="h-full transition-all" style="width: {Math.min(loggedRatio, 1) * 100}%; background: {overBudget ? 'var(--ds-text-danger, #cc3344)' : 'var(--ds-background-brand, #3b82f6)'};"></div>
                </div>
              {/if}
            </div>
          {/if}
          <EmptyState icon={Clock} title={t('items.noTimeLogged')}>
            {#snippet action()}
            <div class="flex justify-center gap-2">
              {#if !activeTimer && getDefaultProjectForTimeLogging()}
                <Button
                  variant="primary"
                  icon={Play}
                  onclick={handleStartTimer}
                  size="small"
                  dataTestid="start-timer-btn"
                  title={t('items.startTimerTitle')}
                  keyboardHint={getShortcutDisplay('itemDetail', 'startTimer')}
                  hotkeyConfig={{ key: toHotkeyString('itemDetail', 'startTimer'), guard: () => tab === 'time' && moduleSettings?.time_tracking_enabled && !!getDefaultProjectForTimeLogging() }}
                >
                  {t('items.startTimer')}
                </Button>
              {/if}
              <Button
                variant="default"
                size="small"
                onclick={handleLogTime}
                title={t('items.logTimeTitle')}
                keyboardHint={getShortcutDisplay('itemDetail', 'logTime')}
                hotkeyConfig={{ key: toHotkeyString('itemDetail', 'logTime'), guard: () => tab === 'time' && moduleSettings?.time_tracking_enabled }}
              >
                {t('items.logTime')}
              </Button>
            </div>
            {/snippet}
          </EmptyState>
        {/if}
      {:else if tab === 'history'}
        <ItemHistory itemId={item.id} />
      {:else if tab === 'agent-log'}
        <ItemAgentLog itemId={item.id} workspaceId={item.workspace_id} />
      {/if}
    </div>
  </div>
</div>
