<script>
  import { onMount } from 'svelte';
  import { AlertTriangle, RefreshCw, TicketCheck } from '@lucide/svelte';
  import { api } from '../api.js';
  import { authStore } from '../stores';
  import { t } from '../stores/i18n.svelte.js';
  import { formatDateTimeLocale, getUserTimezone } from '../utils/dateFormatter.js';
  import {
    getZammadObservedValueLabel,
    getZammadStatusBucketDisplayLabel,
    isCurrentZammadWorkspaceOverviewRequest,
  } from '../utils/zammadObservations.js';
  import WidgetState from './WidgetState.svelte';

  let { workspaceId = null } = $props();

  let overview = $state(null);
  let loading = $state(false);
  let error = $state(null);
  let requestVersion = 0;
  let loadedWorkspaceId = $state(undefined);
  let timezone = $derived(getUserTimezone(authStore.currentUser));
  let hasOverviewData = $derived(
    Boolean(
      overview?.total ||
        overview?.sync_failed ||
        overview?.creation_uncertain ||
        overview?.unknown_status,
    ),
  );

  const metricKeys = [
    ['total', 'total'],
    ['active', 'active'],
    ['closed', 'closed'],
    ['unassigned', 'unassigned'],
    ['sync_failed', 'syncFailed'],
    ['creation_uncertain', 'creationUncertain'],
    ['unknown_status', 'unknownStatus'],
  ];
  const observedTimelineFields = new Set(['status', 'group', 'owner']);
  const overviewRefreshIntervalMs = 120_000;

  $effect(() => {
    if (workspaceId === loadedWorkspaceId) return;
    loadedWorkspaceId = workspaceId;
    void loadOverview(workspaceId);
  });

  onMount(() => {
    const interval = window.setInterval(() => {
      if (workspaceId) void loadOverview(workspaceId);
    }, overviewRefreshIntervalMs);

    return () => window.clearInterval(interval);
  });

  function fieldLabel(field) {
    const fields = {
      status: 'status',
      owner: 'owner',
      group: 'group',
    };
    return t(`zammad.timeline.field.${fields[field]}`);
  }

  function valueLabel(value) {
    return getZammadObservedValueLabel(value, t);
  }

  function statusBucketLabel(status) {
    return getZammadStatusBucketDisplayLabel(status, t);
  }

  function changeLabel(change) {
    return t('zammad.timeline.change', {
      field: fieldLabel(change.field),
      from: valueLabel(change.old_value),
      to: valueLabel(change.new_value),
    });
  }

  async function loadOverview(targetWorkspaceId = workspaceId) {
    const version = ++requestVersion;
    const isCurrentRequest = () =>
      isCurrentZammadWorkspaceOverviewRequest(
        version,
        requestVersion,
        targetWorkspaceId,
        workspaceId,
      );
    if (!targetWorkspaceId) {
      overview = null;
      error = null;
      loading = false;
      return;
    }

    loading = true;
    error = null;
    try {
      const response = await api.zammadTickets.workspaceOverview(targetWorkspaceId, { limit: 5 });
      if (!isCurrentRequest()) return;
      overview = {
        total: 0,
        active: 0,
        closed: 0,
        unassigned: 0,
        sync_failed: 0,
        creation_uncertain: 0,
        unknown_status: 0,
        ...response,
        by_status: Array.isArray(response?.by_status) ? response.by_status : [],
        recent_changes: Array.isArray(response?.recent_changes)
          ? response.recent_changes
              .filter((change) => observedTimelineFields.has(change?.field))
              .slice(0, 5)
          : [],
      };
    } catch (err) {
      if (!isCurrentRequest()) return;
      console.error('Failed to load Zammad support overview:', err);
      overview = null;
      error = t('zammad.overview.loadFailed');
    } finally {
      if (isCurrentRequest()) {
        loading = false;
      }
    }
  }
</script>

<WidgetState
  {loading}
  {error}
  isEmpty={!loading && !error && !hasOverviewData}
  loadingText={t('zammad.overview.loading')}
  emptyIcon={TicketCheck}
  emptyTitle={t('zammad.overview.emptyTitle')}
  emptySubtitle={t('zammad.overview.emptyDescription')}
  onRetry={() => loadOverview(workspaceId)}
>
  {#snippet children()}
    <div class="space-y-4" data-testid="zammad-support-overview">
      <div class="flex items-center justify-between gap-2">
        <p class="text-xs" style="color: var(--ds-text-subtle);">{t('zammad.overview.observedHint')}</p>
        <button
          type="button"
          class="inline-flex flex-shrink-0 items-center gap-1 rounded px-1.5 py-1 text-xs font-medium hover:underline disabled:cursor-not-allowed disabled:opacity-60"
          onclick={() => loadOverview(workspaceId)}
          disabled={loading}
          title={t('zammad.overview.refresh')}
          aria-label={t('zammad.overview.refresh')}
          style="color: var(--ds-link);"
        >
          <RefreshCw class={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} aria-hidden="true" />
          <span>{t('zammad.overview.refresh')}</span>
        </button>
      </div>

      <dl class="grid grid-cols-2 sm:grid-cols-4 gap-2">
        {#each metricKeys as [key, label]}
          <div class="rounded border px-2 py-2" style="border-color: var(--ds-border); background-color: var(--ds-background-neutral);">
            <dt class="text-xs" style="color: var(--ds-text-subtle);">{t(`zammad.overview.metrics.${label}`)}</dt>
            <dd class="mt-1 text-lg font-semibold" style="color: {key === 'sync_failed' && overview[key] > 0 ? 'var(--ds-text-danger)' : 'var(--ds-text)'};">{overview[key]}</dd>
          </div>
        {/each}
      </dl>

      <section aria-labelledby="zammad-status-summary">
        <h4 id="zammad-status-summary" class="text-xs font-semibold" style="color: var(--ds-text);">{t('zammad.overview.byStatus')}</h4>
        <ul class="mt-2 flex flex-wrap gap-1.5">
          {#each overview.by_status as status}
            <li class="rounded px-2 py-1 text-xs" style="background-color: var(--ds-background-neutral); color: var(--ds-text-subtle);">
              {statusBucketLabel(status)}: {status.count}
            </li>
          {/each}
        </ul>
      </section>

      <section aria-labelledby="zammad-recent-changes">
        <h4 id="zammad-recent-changes" class="text-xs font-semibold" style="color: var(--ds-text);">{t('zammad.overview.recentChanges')}</h4>
        {#if overview.recent_changes.length === 0}
          <p class="mt-2 text-xs" style="color: var(--ds-text-subtle);">{t('zammad.overview.noRecentChanges')}</p>
        {:else}
          <ol class="mt-2 space-y-2">
            {#each overview.recent_changes as change (change.id)}
              <li class="flex items-start gap-2 text-xs">
                <TicketCheck class="mt-0.5 h-3.5 w-3.5 flex-shrink-0" style="color: var(--ds-text-subtle);" aria-hidden="true" />
                <div class="min-w-0">
                  <a class="font-medium hover:underline" href={`/workspaces/${workspaceId}/items/${change.item_id}`} style="color: var(--ds-link);">
                    {change.item_key}
                  </a>
                  <span style="color: var(--ds-text-subtle);"> · {t('zammad.ticketNumber', { number: change.ticket_number })}</span>
                  <p style="color: var(--ds-text);">{changeLabel(change)}</p>
                  <time datetime={change.observed_at} style="color: var(--ds-text-subtle);">{formatDateTimeLocale(change.observed_at, timezone)}</time>
                </div>
              </li>
            {/each}
          </ol>
        {/if}
      </section>

      {#if overview.sync_failed > 0 || overview.creation_uncertain > 0 || overview.unknown_status > 0}
        <p class="flex items-start gap-1.5 text-xs" style="color: var(--ds-text-warning);">
          <AlertTriangle class="mt-0.5 h-3.5 w-3.5 flex-shrink-0" aria-hidden="true" />
          {t('zammad.overview.syncStateHint')}
        </p>
      {/if}
    </div>
  {/snippet}
</WidgetState>
