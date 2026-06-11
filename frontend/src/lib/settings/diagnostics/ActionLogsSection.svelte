<script>
  import DataTable from '../../components/DataTable.svelte';
  import { getActionLogs } from '../../api/diagnostics.js';
  import DiagnosticsSection from './DiagnosticsSection.svelte';
  import { formatDurationMs, formatUtcTime, truncateText } from './format-utils.js';

  /** @type {{loading: boolean, error: string|null, failed: any[], slowest: any[]}} */
  let view = $state({ loading: true, error: null, failed: [], slowest: [] });
  let lastRefreshed = $state(null);

  async function load() {
    view = { ...view, loading: true, error: null };
    try {
      const [failed, slowest] = await Promise.all([
        getActionLogs({ mode: 'failed', since: '24h', limit: 25 }),
        getActionLogs({ mode: 'slowest', since: '24h', limit: 10 }),
      ]);
      view = { loading: false, error: null, failed: failed ?? [], slowest: slowest ?? [] };
      lastRefreshed = new Date();
    } catch (err) {
      view = { ...view, loading: false, error: err?.message ?? String(err) };
    }
  }

  const failedColumns = [
    { key: 'started_at', label: 'When', render: (log) => formatUtcTime(log.started_at), textColor: 'var(--ds-text-subtle)' },
    { key: 'action_name', label: 'Action', render: (log) => log.action_name || `#${log.action_id}` },
    { key: 'workspace_name', label: 'Workspace', render: (log) => log.workspace_name || '—', textColor: 'var(--ds-text-subtle)' },
    { key: 'item_title', label: 'Item', render: (log) => log.item_title || (log.item_id ? `#${log.item_id}` : '—'), textColor: 'var(--ds-text-subtle)' },
    { key: 'error_message', label: 'Error', slot: 'error' },
    { key: 'duration_ms', label: 'Duration', render: (log) => formatDurationMs(log.duration_ms), align: 'text-right', textColor: 'var(--ds-text-subtle)' },
  ];

  const slowestColumns = [
    { key: 'started_at', label: 'When', render: (log) => formatUtcTime(log.started_at), textColor: 'var(--ds-text-subtle)' },
    { key: 'action_name', label: 'Action', render: (log) => log.action_name || `#${log.action_id}` },
    { key: 'workspace_name', label: 'Workspace', render: (log) => log.workspace_name || '—', textColor: 'var(--ds-text-subtle)' },
    { key: 'status', label: 'Status', textColor: 'var(--ds-text-subtle)' },
    { key: 'duration_ms', label: 'Duration', render: (log) => formatDurationMs(log.duration_ms), align: 'text-right' },
  ];
</script>

<DiagnosticsSection
  title="Action executions (last 24h)"
  subtitle="Recent failures and slowest completed runs across all workspaces. Auto-refreshes every 30s."
  dataTestId="diagnostics-action-logs"
  onLoad={load}
  lastRefreshed={lastRefreshed}
  bind:loading={view.loading}
  bind:error={view.error}
>
  {#snippet children()}
    <div>
      <h4 class="text-sm font-semibold mb-2" style="color: var(--ds-text);">Recent failures</h4>
      <DataTable
        columns={failedColumns}
        data={view.failed}
        keyField="id"
        emptyMessage="No failed action executions in the last 24h."
      >
        {#snippet error(log)}
          <span style="color: var(--ds-text);" title={log.error_message}>
            {truncateText(log.error_message, 80) || '—'}
          </span>
        {/snippet}
      </DataTable>
    </div>

    <div>
      <h4 class="text-sm font-semibold mb-2" style="color: var(--ds-text);">Slowest completed runs</h4>
      <DataTable
        columns={slowestColumns}
        data={view.slowest}
        keyField="id"
        emptyMessage="No completed action executions in the last 24h."
      />
    </div>

  {/snippet}
</DiagnosticsSection>
