<script>
  import {
    IconAlertTriangle,
    IconAlertCircle,
    IconCircleCheck,
    IconCloud,
    IconDatabase,
  } from '@tabler/icons-svelte-runes';
  import Card from '../../components/Card.svelte';
  import StatCard from '../../components/StatCard.svelte';
  import DataTable from '../../components/DataTable.svelte';
  import { getLLMProviderStatus, getBriefingFailures } from '../../api/diagnostics.js';
  import { api } from '../../api.js';
  import { errorToast, successToast } from '../../stores/toasts.svelte.js';
  import DiagnosticsSection from './DiagnosticsSection.svelte';
  import { formatRelativeTime, formatUtcTime, truncateText } from './format-utils.js';

  let view = $state({
    loading: true,
    error: null,
    providers: [],
    failures: { since: '24h', buckets: [], recent: [] },
  });
  let lastRefreshed = $state(null);
  let refreshing = $state({});

  async function load() {
    view = { ...view, loading: true, error: null };
    try {
      const [providers, failures] = await Promise.all([
        getLLMProviderStatus(),
        getBriefingFailures({ since: '24h' }),
      ]);
      view = {
        loading: false,
        error: null,
        providers: providers ?? [],
        failures: failures ?? { since: '24h', buckets: [], recent: [] },
      };
      lastRefreshed = new Date();
    } catch (err) {
      view = { ...view, loading: false, error: err?.message ?? String(err) };
    }
  }

  async function refreshProvider(type) {
    refreshing = { ...refreshing, [type]: true };
    try {
      const result = await api.llmProviders.refreshModels(type);
      successToast(`Fetched ${result.models?.length ?? 0} models from ${type}`);
      await load();
    } catch (err) {
      errorToast(err?.message ?? `Refresh failed for ${type}`);
      await load();
    } finally {
      refreshing = { ...refreshing, [type]: false };
    }
  }

  const BUCKET_LABELS = {
    model_not_found: 'Model not found',
    auth_failed: 'Auth failed',
    rate_limited: 'Rate limited',
    server_error: 'Server error',
    connection_failed: 'Connection failed',
    other: 'Other',
  };
  const BUCKET_COLORS = {
    model_not_found: 'orange',
    auth_failed: 'orange',
    rate_limited: 'blue',
    server_error: 'orange',
    connection_failed: 'orange',
    other: 'blue',
  };

  const driftRows = $derived(
    (view.providers ?? []).flatMap((p) =>
      (p.connections ?? [])
        .filter((c) => c.model_still_in_catalog === false)
        .map((c) => ({ provider: p.name, providerType: p.type, ...c }))
    )
  );

  const totalFailures = $derived(
    (view.failures?.buckets ?? []).reduce((acc, b) => acc + (b.count ?? 0), 0)
  );

  const recentColumns = [
    { key: 'created_at', label: 'When', render: (row) => formatUtcTime(row.created_at) },
    { key: 'user_id', label: 'User', render: (row) => `#${row.user_id}` },
    { key: 'date', label: 'Briefing date', render: (row) => row.date },
    {
      key: 'classified_as',
      label: 'Class',
      render: (row) => BUCKET_LABELS[row.classified_as] ?? row.classified_as,
    },
    {
      key: 'error',
      label: 'Error',
      render: (row) => truncateText(row.error, 200),
      textColor: 'var(--ds-text-subtle)',
    },
  ];
</script>

<DiagnosticsSection
  title="AI / LLM health"
  subtitle="Per-provider model catalog cache and recent briefing failures grouped by error class. Refresh a catalog when a provider releases or retires models — drift is then visible against the connections you have configured."
  dataTestId="diagnostics-llm-health"
  onLoad={load}
  lastRefreshed={lastRefreshed}
  bind:loading={view.loading}
  bind:error={view.error}
>
  {#snippet children()}
    {#if driftRows.length > 0}
      <Card>
        <div class="flex items-start gap-3 p-4" data-testid="llm-health-drift">
          <IconAlertTriangle class="w-6 h-6 flex-shrink-0 mt-0.5" style="color: var(--ds-accent-orange);" />
          <div class="flex-1">
            <div class="font-semibold" style="color: var(--ds-text);">Model drift detected</div>
            <div class="text-sm mt-1 mb-2" style="color: var(--ds-text-subtle);">
              {driftRows.length} enabled connection{driftRows.length === 1 ? '' : 's'} reference a model that is no longer in the provider's refreshed catalog. Update the connection or refresh the catalog.
            </div>
            <ul class="text-sm space-y-1 mt-2" style="color: var(--ds-text);">
              {#each driftRows as row (`${row.providerType}:${row.id}`)}
                <li>
                  <strong>{row.name}</strong>
                  <span style="color: var(--ds-text-subtle);">({row.provider})</span>
                  references <code>{row.model}</code>
                </li>
              {/each}
            </ul>
          </div>
        </div>
      </Card>
    {/if}

    <div>
      <h4 class="text-sm font-semibold mb-3 flex items-center gap-1.5" style="color: var(--ds-text);">
        <IconCloud class="w-4 h-4" />
        Provider model catalogs
      </h4>
      <div class="space-y-2">
        {#each view.providers as p (p.type)}
          <Card>
            <div class="flex items-center gap-4 p-3">
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2">
                  <span class="font-semibold" style="color: var(--ds-text);">{p.name}</span>
                  <span class="text-xs px-1.5 py-0.5 rounded" style="background-color: var(--ds-surface-raised); color: var(--ds-text-subtle);">{p.type}</span>
                  {#if !p.has_dynamic_models}
                    <span class="text-xs" style="color: var(--ds-text-subtle);">static catalog</span>
                  {/if}
                </div>
                <div class="text-xs mt-1" style="color: var(--ds-text-subtle);">
                  {#if p.has_dynamic_models}
                    {p.models_cached_count} cached · last refresh {formatRelativeTime(p.last_refreshed_at)}
                    {#if p.connections.length > 0}
                      · {p.connections.length} enabled connection{p.connections.length === 1 ? '' : 's'}
                    {/if}
                  {:else}
                    {p.connections.length} enabled connection{p.connections.length === 1 ? '' : 's'}
                  {/if}
                </div>
                {#if p.last_error}
                  <div class="flex items-start gap-1.5 text-xs mt-1.5" style="color: var(--ds-accent-orange);">
                    <IconAlertCircle class="w-3.5 h-3.5 flex-shrink-0 mt-0.5" />
                    <span class="break-words">{p.last_error}</span>
                  </div>
                {/if}
              </div>
              {#if p.has_dynamic_models}
                <button
                  type="button"
                  class="inline-flex items-center gap-1.5 text-sm px-2.5 py-1.5 rounded-md transition-colors flex-shrink-0"
                  style="color: var(--ds-text); background-color: var(--ds-surface-raised); border: 1px solid var(--ds-border-subtle);"
                  onclick={() => refreshProvider(p.type)}
                  disabled={refreshing[p.type]}
                >
                  {refreshing[p.type] ? 'Refreshing…' : 'Refresh catalog'}
                </button>
              {/if}
            </div>
          </Card>
        {/each}
      </div>
    </div>

    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4" data-testid="llm-health-failure-buckets">
      {#each view.failures?.buckets ?? [] as bucket (`bucket-${bucket.classified_as}`)}
        <StatCard
          icon={IconDatabase}
          label={BUCKET_LABELS[bucket.classified_as] ?? bucket.classified_as}
          value={String(bucket.count)}
          color={BUCKET_COLORS[bucket.classified_as] ?? 'blue'}
        />
      {/each}
    </div>

    {#if totalFailures === 0}
      <p class="text-sm" style="color: var(--ds-text-subtle);">
        No briefing failures in the last 24h.
      </p>
    {:else}
      <div>
        <h4 class="text-sm font-semibold mb-2 flex items-center gap-1.5" style="color: var(--ds-text);">
          <IconDatabase class="w-4 h-4" />
          Recent failure details
        </h4>
        <DataTable
          columns={recentColumns}
          data={view.failures?.recent ?? []}
          keyField="id"
          defaultPageSize={10}
        />
      </div>
    {/if}
  {/snippet}
</DiagnosticsSection>
