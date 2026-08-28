<script>
  import DataTable from '../../components/DataTable.svelte';
  import {
    getDomainEventDiagnostics,
    replayDomainEvent,
    skipDomainEvent,
  } from '../../api/diagnostics.js';
  import { errorToast, successToast } from '../../stores/toasts.svelte.js';
  import DiagnosticsSection from './DiagnosticsSection.svelte';
  import { formatAgeSeconds, formatUtcTime, truncateText } from './format-utils.js';

  /** @type {{loading: boolean, error: string|null, data: any|null}} */
  let view = $state({ loading: true, error: null, data: null });
  let lastRefreshed = $state(null);
  let consumerKey = $state('');
  let workspaceId = $state('');
  let reasons = $state({});
  let performing = $state({});

  function deliveryKey(row) {
    return `${row.event_id}:${row.consumer_key}`;
  }

  async function load() {
    view = { ...view, loading: true, error: null };
    try {
      const normalizedConsumerKey = String(consumerKey ?? '').trim();
      const normalizedWorkspaceId = String(workspaceId ?? '').trim();
      const data = await getDomainEventDiagnostics({
        consumerKey: normalizedConsumerKey || undefined,
        workspaceId: normalizedWorkspaceId || undefined,
        limit: 100,
      });
      view = { loading: false, error: null, data };
      lastRefreshed = new Date();
    } catch (err) {
      view = { ...view, loading: false, error: err?.message ?? String(err) };
    }
  }

  async function changeDelivery(row, action) {
    const key = deliveryKey(row);
    const reason = reasons[key]?.trim();
    if (!reason) {
      errorToast('Enter an operator reason before replaying or skipping a delivery');
      return;
    }
    if (action === 'skip' && !confirm('Skip this failed delivery and unblock later events for its aggregate?')) {
      return;
    }
    performing[key] = action;
    try {
      const result = action === 'replay'
        ? await replayDomainEvent(row.event_id, row.consumer_key, reason)
        : await skipDomainEvent(row.event_id, row.consumer_key, reason);
      successToast(result.ordering_impact);
      delete reasons[key];
      await load();
    } catch (err) {
      errorToast(err?.message ?? `Failed to ${action} delivery`);
    } finally {
      delete performing[key];
    }
  }

  const consumerColumns = [
    { key: 'consumer_key', label: 'Consumer' },
    { key: 'pending', label: 'Pending', align: 'text-right' },
    { key: 'retrying', label: 'Retrying', align: 'text-right' },
    { key: 'active_leases', label: 'Active leases', align: 'text-right' },
    { key: 'expired_leases', label: 'Expired leases', align: 'text-right' },
    { key: 'terminal_failures', label: 'Failed', align: 'text-right' },
    { key: 'blocked_aggregates', label: 'Blocked', align: 'text-right' },
    { key: 'oldest_pending_age_seconds', label: 'Oldest', render: (row) => formatAgeSeconds(row.oldest_pending_age_seconds), align: 'text-right' },
  ];

  const failureColumns = [
    { key: 'failed_at', label: 'Failed at', render: (row) => formatUtcTime(row.failed_at), textColor: 'var(--ds-text-subtle)' },
    { key: 'consumer_key', label: 'Consumer' },
    { key: 'event_type', label: 'Event' },
    { key: 'aggregate_id', label: 'Aggregate', render: (row) => `${row.aggregate_type}/${row.aggregate_id} #${row.aggregate_sequence}` },
    { key: 'workspace_id', label: 'Workspace', render: (row) => row.workspace_id ?? 'global', align: 'text-right' },
    { key: 'last_error', label: 'Error', slot: 'lastError' },
    { key: 'operator_action', label: 'Operator action', slot: 'operatorAction', width: '22rem' },
  ];
</script>

<DiagnosticsSection
  title="Durable domain events"
  subtitle="Database-backed delivery health. Replay retries a terminal failure; skip records an explicit exception and unblocks later events for the same consumer and aggregate."
  dataTestId="diagnostics-domain-events"
  onLoad={load}
  lastRefreshed={lastRefreshed}
  bind:loading={view.loading}
  bind:error={view.error}
>
  {#snippet children()}
    <form class="flex flex-wrap items-end gap-3" onsubmit={(event) => { event.preventDefault(); load(); }}>
      <label class="text-sm" style="color: var(--ds-text);">
        Consumer
        <input
          data-testid="domain-events-consumer-filter"
          class="block mt-1 px-3 py-2 rounded-md border bg-transparent"
          style="border-color: var(--ds-border);"
          bind:value={consumerKey}
          placeholder="all consumers"
        />
      </label>
      <label class="text-sm" style="color: var(--ds-text);">
        Workspace ID
        <input
          data-testid="domain-events-workspace-filter"
          class="block mt-1 px-3 py-2 rounded-md border bg-transparent"
          style="border-color: var(--ds-border);"
          type="number"
          min="1"
          bind:value={workspaceId}
          placeholder="all workspaces"
        />
      </label>
      <button
        data-testid="domain-events-apply-filter"
        type="submit"
        class="px-3 py-2 text-sm rounded-md border"
        style="border-color: var(--ds-border); background-color: var(--ds-surface-raised); color: var(--ds-text);"
      >Apply filter</button>
    </form>

    <div>
      <h4 class="text-sm font-semibold mb-2" style="color: var(--ds-text);">Consumer health</h4>
      <DataTable
        columns={consumerColumns}
        data={view.data?.consumers ?? []}
        keyField="consumer_key"
        emptyMessage="No durable consumers match this filter."
      />
    </div>

    <div>
      <h4 class="text-sm font-semibold mb-2" style="color: var(--ds-text);">Terminal failures</h4>
      <DataTable
        columns={failureColumns}
        data={view.data?.failures ?? []}
        keyField="event_key"
        emptyMessage="No terminal delivery failures match this filter."
      >
        {#snippet lastError(row)}
          <span title={row.last_error}>{truncateText(row.last_error, 80) || '—'}</span>
        {/snippet}
        {#snippet operatorAction(row)}
          <div class="flex items-center gap-2">
            <input
              data-testid={`domain-event-reason-${row.event_id}`}
              class="min-w-0 flex-1 px-2 py-1.5 text-sm rounded-md border bg-transparent"
              style="border-color: var(--ds-border);"
              bind:value={reasons[deliveryKey(row)]}
              placeholder="Required reason"
            />
            <button
              data-testid={`domain-event-replay-${row.event_id}`}
              type="button"
              class="px-2 py-1.5 text-sm rounded-md border"
              style="border-color: var(--ds-border); color: var(--ds-text);"
              disabled={performing[deliveryKey(row)]}
              onclick={() => changeDelivery(row, 'replay')}
            >Replay</button>
            <button
              data-testid={`domain-event-skip-${row.event_id}`}
              type="button"
              class="px-2 py-1.5 text-sm rounded-md border"
              style="border-color: var(--ds-border-danger); color: var(--ds-text-danger);"
              disabled={performing[deliveryKey(row)]}
              onclick={() => changeDelivery(row, 'skip')}
            >Skip</button>
          </div>
        {/snippet}
      </DataTable>
    </div>
  {/snippet}
</DiagnosticsSection>
