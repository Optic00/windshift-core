<script>
  import { onDestroy, onMount } from 'svelte';
  import {
    IconRefresh,
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

  let interval;
  onMount(() => {
    load();
    interval = setInterval(load, 30_000);
  });
  onDestroy(() => {
    if (interval) clearInterval(interval);
  });

  function fmtTime(iso) {
    if (!iso) return '—';
    const d = iso instanceof Date ? iso : new Date(iso);
    if (Number.isNaN(d.getTime())) return '—';
    return d.toISOString().replace('T', ' ').replace(/\..*Z$/, ' UTC');
  }

  function fmtRelative(iso) {
    if (!iso) return 'Never refreshed';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return 'Never refreshed';
    const diffMs = Date.now() - d.getTime();
    const mins = Math.round(diffMs / 60000);
    if (mins < 1) return 'just now';
    if (mins < 60) return `${mins}m ago`;
    const hours = Math.round(mins / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.round(hours / 24);
    return `${days}d ago`;
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
    { key: 'created_at', label: 'When', render: (row) => fmtTime(row.created_at) },
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
      render: (row) => (row.error?.length > 200 ? row.error.slice(0, 200) + '…' : row.error),
      textColor: 'var(--ds-text-subtle)',
    },
  ];
</script>

<section class="space-y-6" data-testid="diagnostics-llm-health">
  <div class="flex items-start justify-between gap-4">
    <div>
      <h3 class="text-base font-semibold" style="color: var(--ds-text);">AI / LLM health</h3>
      <p class="text-sm" style="color: var(--ds-text-subtle);">
        Per-provider model catalog cache and recent briefing failures grouped by error class. Refresh a catalog when a provider releases or retires models — drift is then visible against the connections you have configured.
      </p>
    </div>
    <button
      type="button"
      class="inline-flex items-center gap-1.5 text-sm px-2.5 py-1.5 rounded-md transition-colors"
      style="color: var(--ds-text-subtle); background-color: var(--ds-surface-raised); border: 1px solid var(--ds-border-subtle);"
      onclick={load}
      disabled={view.loading}
      data-testid="llm-health-refresh"
    >
      <IconRefresh class="w-4 h-4" />
      <span>{view.loading ? 'Loading…' : 'Refresh'}</span>
    </button>
  </div>

  {#if view.error}
    <Card>
      <div class="flex items-start gap-3 p-3" style="color: var(--ds-accent-red);">
        <IconAlertTriangle class="w-5 h-5 flex-shrink-0 mt-0.5" />
        <div>
          <div class="font-semibold">Failed to load diagnostics</div>
          <div class="text-sm" style="color: var(--ds-text-subtle);">{view.error}</div>
        </div>
      </div>
    </Card>
  {:else}
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
                    {p.models_cached_count} cached · last refresh {fmtRelative(p.last_refreshed_at)}
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
                  disabled={!!refreshing[p.type]}
                  data-testid={`llm-health-refresh-${p.type}`}
                >
                  <IconRefresh class="w-4 h-4 {refreshing[p.type] ? 'animate-spin' : ''}" />
                  <span>{refreshing[p.type] ? 'Refreshing…' : 'Refresh now'}</span>
                </button>
              {/if}
            </div>
          </Card>
        {/each}
      </div>
    </div>

    <div>
      <div class="flex items-baseline justify-between mb-3">
        <h4 class="text-sm font-semibold flex items-center gap-1.5" style="color: var(--ds-text);">
          <IconDatabase class="w-4 h-4" />
          Briefing failures (last 24h)
        </h4>
        <span class="text-xs" style="color: var(--ds-text-subtle);">Last refreshed {fmtTime(lastRefreshed)}</span>
      </div>
      {#if totalFailures === 0}
        <Card>
          <div class="flex items-center gap-3 p-3">
            <IconCircleCheck class="w-5 h-5" style="color: var(--ds-accent-green);" />
            <span class="text-sm" style="color: var(--ds-text);">No failed briefings in the last 24 hours.</span>
          </div>
        </Card>
      {:else}
        <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-3 mb-4">
          {#each view.failures.buckets as b (b.class)}
            <StatCard
              icon={b.count > 0 ? IconAlertCircle : IconCircleCheck}
              label={BUCKET_LABELS[b.class] ?? b.class}
              value={b.count}
              color={b.count > 0 ? BUCKET_COLORS[b.class] ?? 'blue' : 'green'}
            />
          {/each}
        </div>
        <DataTable
          columns={recentColumns}
          data={view.failures.recent}
          keyField="id"
          emptyMessage="No failed briefings to show."
        />
      {/if}
    </div>
  {/if}
</section>
