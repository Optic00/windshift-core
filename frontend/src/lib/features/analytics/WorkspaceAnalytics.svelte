<script>
  import { onMount } from 'svelte';
  import { RefreshCw } from '@lucide/svelte';
  import { api } from '../../api.js';
  import { navigate } from '../../router.js';
  import { t } from '../../stores/i18n.svelte.js';
  import Button from '../../components/Button.svelte';
  import Card from '../../components/Card.svelte';
  import Input from '../../components/Input.svelte';
  import Label from '../../components/Label.svelte';
  import Select from '../../components/Select.svelte';
  import Text from '../../components/Text.svelte';
  import PageHeader from '../../layout/PageHeader.svelte';
  import Chart from '../../widgets/Chart.svelte';
  import {
    defaultAnalyticsRange,
    formatDateOnly,
    formatDayNumber,
    inclusiveDateRangeDays,
    localDateString,
    shiftDateString,
    validateAnalyticsRange,
  } from './analyticsView.js';

  let { workspaceId = null } = $props();

  const initialRange = defaultAnalyticsRange();
  let initialized = $state(false);
  let loading = $state(true);
  let analyticsData = $state(null);
  let loadError = $state('');
  let validationCode = $state(null);
  let collections = $state([]);
  let collectionLoadError = $state(false);
  let selectedCollection = $state('');
  let selectedPreset = $state('84');
  let startDate = $state(initialRange.startDate);
  let endDate = $state(initialRange.endDate);

  let analyticsLoadVersion = 0;
  let collectionsLoadVersion = 0;
  let lastAnalyticsLoadKey = null;
  let lastCollectionsWorkspace = null;

  const collectionOptions = $derived([
    { value: '', label: t('analytics.allItems') },
    ...collections.map((collection) => ({
      value: String(collection.id),
      label: collection.name,
    })),
  ]);

  const rangeOptions = $derived([
    { value: '30', label: t('analytics.range.last30Days') },
    { value: '84', label: t('analytics.range.last12Weeks') },
    { value: '180', label: t('analytics.range.last6Months') },
    { value: '365', label: t('analytics.range.lastYear') },
    { value: 'custom', label: t('analytics.range.custom') },
  ]);

  const analyticsLoadKey = $derived(
    initialized && workspaceId
      ? `${workspaceId}|${selectedCollection}|${startDate}|${endDate}`
      : null,
  );

  $effect(() => {
    const currentWorkspace = initialized && workspaceId ? String(workspaceId) : null;
    if (!currentWorkspace || currentWorkspace === lastCollectionsWorkspace) return;

    if (lastCollectionsWorkspace !== null) {
      selectedCollection = '';
    }
    lastCollectionsWorkspace = currentWorkspace;
    analyticsData = null;
    loadCollections(currentWorkspace);
  });

  $effect(() => {
    const key = analyticsLoadKey;
    if (!key || key === lastAnalyticsLoadKey) return;
    lastAnalyticsLoadKey = key;
    loadAnalytics();
  });

  onMount(() => {
    if (typeof window !== 'undefined') {
      const query = new URLSearchParams(window.location.search);
      const queryStart = query.get('start_date');
      const queryEnd = query.get('end_date');
      const queryCollection = query.get('collection_id');
      if (queryStart) startDate = queryStart;
      if (queryEnd) endDate = queryEnd;
      if (queryCollection && /^\d+$/.test(queryCollection)) {
        selectedCollection = queryCollection;
      }
      selectedPreset = detectPreset(startDate, endDate);
    }
    initialized = true;
  });

  async function loadCollections(targetWorkspace) {
    const version = ++collectionsLoadVersion;
    collectionLoadError = false;
    try {
      const response = await api.collections.getAll({ workspace_id: targetWorkspace });
      if (version !== collectionsLoadVersion) return;
      collections = Array.isArray(response) ? response : response?.items || [];
      if (
        selectedCollection &&
        !collections.some((collection) => String(collection.id) === String(selectedCollection))
      ) {
        selectedCollection = '';
      }
    } catch (error) {
      if (version !== collectionsLoadVersion) return;
      console.error('Failed to load analytics collections:', error);
      collections = [];
      selectedCollection = '';
      collectionLoadError = true;
    }
  }

  async function loadAnalytics() {
    const version = ++analyticsLoadVersion;
    const rangeError = validateAnalyticsRange(startDate, endDate);
    validationCode = rangeError;
    loadError = '';
    if (rangeError || !workspaceId) {
      loading = false;
      return;
    }

    loading = true;
    analyticsData = null;
    syncQueryString();
    try {
      const params = { start_date: startDate, end_date: endDate };
      if (selectedCollection) params.collection_id = selectedCollection;
      const response = await api.analytics.getAnalytics(workspaceId, params);
      if (version !== analyticsLoadVersion) return;
      if (response?.schema_version !== 2) {
        throw new Error(t('analytics.unsupportedVersion'));
      }
      analyticsData = response;
    } catch (error) {
      if (version !== analyticsLoadVersion) return;
      console.error('Failed to load analytics:', error);
      loadError = error?.message || t('analytics.errorTitle');
    } finally {
      if (version === analyticsLoadVersion) loading = false;
    }
  }

  function syncQueryString() {
    if (typeof window === 'undefined') return;
    const url = new URL(window.location.href);
    url.searchParams.set('start_date', startDate);
    url.searchParams.set('end_date', endDate);
    if (selectedCollection) {
      url.searchParams.set('collection_id', selectedCollection);
    } else {
      url.searchParams.delete('collection_id');
    }
    window.history.replaceState(window.history.state, '', url);
  }

  function applyPreset(value) {
    selectedPreset = value;
    if (value === 'custom') return;
    const days = Number(value);
    endDate = localDateString();
    startDate = shiftDateString(endDate, -(days - 1));
  }

  function detectPreset(from, to) {
    const days = inclusiveDateRangeDays(from, to);
    return [30, 84, 180, 365].includes(days) ? String(days) : 'custom';
  }

  function handleDateEdit() {
    selectedPreset = detectPreset(startDate, endDate);
  }

  function retry() {
    lastAnalyticsLoadKey = null;
    loadAnalytics();
  }

  function openItem(item) {
    navigate(`/workspaces/${workspaceId}/items/${item.id}`);
  }

  function days(value) {
    return t('analytics.daysValue', { value: formatDayNumber(value) });
  }

  function period(bucket) {
    return `${formatDateOnly(bucket.start_date)} – ${formatDateOnly(bucket.end_date, {
      year: 'numeric',
    })}`;
  }

  function signed(value) {
    return value > 0 ? `+${value}` : String(value);
  }

  const dataset = $derived(analyticsData?.dataset || null);
  const health = $derived(analyticsData?.health || null);
  const throughput = $derived(analyticsData?.throughput || null);
  const aging = $derived(analyticsData?.aging_wip || null);
  const deliveryTime = $derived(analyticsData?.delivery_time || null);

  const healthMetrics = $derived.by(() => [
    { key: 'unfinished', label: t('analytics.health.unfinished'), value: health?.unfinished_items || 0 },
    { key: 'overdue', label: t('analytics.health.overdue'), value: health?.overdue || 0 },
    { key: 'stale', label: t('analytics.health.stale'), value: health?.stale || 0 },
    { key: 'unassigned', label: t('analytics.health.unassigned'), value: health?.unassigned || 0 },
    {
      key: 'without-priority',
      label: t('analytics.health.withoutPriority'),
      value: health?.without_priority || 0,
    },
    {
      key: 'without-estimate',
      label: t('analytics.health.withoutEstimate'),
      value: health?.without_estimate || 0,
    },
  ]);

  const throughputBuckets = $derived(throughput?.buckets || []);
  const throughputCategories = $derived(
    throughputBuckets.map((bucket) => formatDateOnly(bucket.start_date)),
  );
  const throughputSeries = $derived([
    {
      key: 'created',
      label: t('analytics.throughput.created'),
      color: '#8b5cf6',
      values: throughputBuckets.map((bucket) => bucket.created),
      showArea: false,
    },
    {
      key: 'completed',
      label: t('analytics.throughput.completed'),
      color: '#10b981',
      values: throughputBuckets.map((bucket) => bucket.completed),
      showArea: false,
    },
  ]);

  const agingBuckets = $derived(aging?.buckets || []);
  const agingCategories = $derived(
    agingBuckets.map((bucket) => t(`analytics.aging.buckets.${bucket.key}`)),
  );
  const agingSeries = $derived([
    {
      key: 'items',
      label: t('analytics.aging.itemCount'),
      color: '#f59e0b',
      values: agingBuckets.map((bucket) => bucket.item_count),
    },
  ]);

  const deliveryTrend = $derived(deliveryTime?.trend || []);
  const deliveryChartTrend = $derived(
    deliveryTrend.filter((point) => point.completed_items > 0),
  );
  const deliveryCategories = $derived(
    deliveryChartTrend.map((point) => formatDateOnly(point.start_date)),
  );
  const deliverySeries = $derived([
    {
      key: 'median',
      label: t('analytics.deliveryTime.median'),
      color: '#3b82f6',
      values: deliveryChartTrend.map((point) => point.median_days),
      showArea: false,
    },
    {
      key: 'p85',
      label: t('analytics.deliveryTime.p85'),
      color: '#06b6d4',
      values: deliveryChartTrend.map((point) => point.p85_days),
      dashed: true,
      showArea: false,
    },
  ]);
</script>

<div class="analytics-page min-h-screen" style="background-color: var(--ds-surface);">
  <div class="p-4 sm:p-6">
    <PageHeader title={t('analytics.title')} subtitle={t('analytics.subtitle')}>
      {#snippet actions()}
        <div class="analytics-filters">
          <div class="filter-field collection-filter">
            <Label for="analytics-collection" size="xs" color="subtle">
              {t('analytics.collection')}
            </Label>
            <Select
              id="analytics-collection"
              options={collectionOptions}
              bind:value={selectedCollection}
              size="small"
              disabled={collectionLoadError}
            />
          </div>
          <div class="filter-field preset-filter">
            <Label for="analytics-range" size="xs" color="subtle">
              {t('analytics.dateRange')}
            </Label>
            <Select
              id="analytics-range"
              options={rangeOptions}
              value={selectedPreset}
              onchange={applyPreset}
              size="small"
            />
          </div>
          <div class="filter-field date-filter">
            <Label for="analytics-start-date" size="xs" color="subtle">
              {t('analytics.from')}
            </Label>
            <Input
              id="analytics-start-date"
              type="date"
              size="small"
              max={endDate}
              bind:value={startDate}
              onchange={handleDateEdit}
            />
          </div>
          <div class="filter-field date-filter">
            <Label for="analytics-end-date" size="xs" color="subtle">
              {t('analytics.to')}
            </Label>
            <Input
              id="analytics-end-date"
              type="date"
              size="small"
              min={startDate}
              bind:value={endDate}
              onchange={handleDateEdit}
            />
          </div>
        </div>
      {/snippet}
    </PageHeader>

    {#if collectionLoadError}
      <div class="notice notice-warning mb-4" role="status">
        {t('analytics.collectionLoadError')}
      </div>
    {/if}

    {#if validationCode}
      <div class="notice notice-error mb-4" role="alert">
        {t(`analytics.validation.${validationCode}`)}
      </div>
    {:else if loadError}
      <Card variant="outlined" padding="default" class="mb-5 error-card">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div class="font-semibold" style="color: var(--ds-text-danger);">
              {t('analytics.errorTitle')}
            </div>
            <Text variant="subtle" size="sm">{loadError}</Text>
          </div>
          <Button variant="default" size="small" icon={RefreshCw} onclick={retry}>
            {t('analytics.retry')}
          </Button>
        </div>
      </Card>
    {:else if loading}
      <div class="flex items-center justify-center py-16" role="status">
        <Text variant="subtle" size="sm">{t('analytics.loading')}</Text>
      </div>
    {:else if analyticsData}
      {#if dataset}
        <div class="scope-banner mb-6">
          <div>
            <div class="text-sm font-semibold" style="color: var(--ds-text);">
              {dataset.cohort_mode === 'current_collection'
                ? t('analytics.scope.currentCollection')
                : t('analytics.scope.currentWorkspace')}
            </div>
            <Text variant="subtle" size="sm">
              {t('analytics.scope.summary', {
                items: dataset.total_items,
                from: formatDateOnly(dataset.date_from, { year: 'numeric' }),
                to: formatDateOnly(dataset.date_to, { year: 'numeric' }),
              })}
            </Text>
          </div>
          <div class="scope-note">
            <Text variant="subtle" size="xs">
              {dataset.cohort_mode === 'current_collection'
                ? t('analytics.scope.currentCollectionNote')
                : t('analytics.scope.currentWorkspaceNote')}
            </Text>
          </div>
        </div>
      {/if}

      <section class="analytics-section" aria-labelledby="health-heading">
        <div class="section-heading">
          <div>
            <h2 id="health-heading">{t('analytics.health.title')}</h2>
            <Text variant="subtle" size="sm">{t('analytics.health.description')}</Text>
          </div>
          {#if health?.stale_after_days}
            <Text variant="subtle" size="xs">
              {t('analytics.health.staleHint', { days: health.stale_after_days })}
            </Text>
          {/if}
        </div>

        <div class="metric-grid">
          {#each healthMetrics as metric (metric.key)}
            <div class="metric-card">
              <div class="metric-value">{metric.value}</div>
              <div class="metric-label">{metric.label}</div>
            </div>
          {/each}
        </div>

        <Card variant="raised" padding="none">
          {#snippet header()}
            <h3>{t('analytics.health.attentionItems')}</h3>
          {/snippet}
          {#if health?.attention_items?.length}
            <div class="table-scroll">
              <table class="analytics-table">
                <thead>
                  <tr>
                    <th>{t('analytics.health.item')}</th>
                    <th>{t('analytics.health.status')}</th>
                    <th class="number-cell">{t('analytics.health.age')}</th>
                    <th>{t('analytics.health.signals')}</th>
                  </tr>
                </thead>
                <tbody>
                  {#each health.attention_items as item (item.id)}
                    <tr>
                      <td>
                        <button type="button" class="item-link" onclick={() => openItem(item)}>
                          <span class="item-number">#{item.workspace_item_number}</span>
                          {item.title}
                        </button>
                      </td>
                      <td>{item.status || '—'}</td>
                      <td class="number-cell">{days(item.age_days)}</td>
                      <td>
                        <div class="flag-list">
                          {#each item.flags as flag}
                            <span class="flag">{t(`analytics.health.flags.${flag}`)}</span>
                          {/each}
                        </div>
                      </td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {:else}
            <div class="empty-copy">{t('analytics.health.allClear')}</div>
          {/if}
        </Card>
      </section>

      <section class="analytics-section flow-grid" aria-label={t('analytics.throughput.title')}>
        <Card variant="raised" padding="default">
          {#snippet header()}
            <div>
              <h3>{t('analytics.throughput.title')}</h3>
              <Text variant="subtle" size="xs">{t('analytics.throughput.description')}</Text>
            </div>
          {/snippet}
          <div class="summary-strip">
            <div>
              <span>{t('analytics.throughput.created')}</span>
              <strong>{throughput?.total_created || 0}</strong>
            </div>
            <div>
              <span>{t('analytics.throughput.completed')}</span>
              <strong>{throughput?.total_completed || 0}</strong>
            </div>
            <div>
              <span>{t('analytics.throughput.average')}</span>
              <strong>{formatDayNumber(throughput?.average_completed || 0)}</strong>
            </div>
          </div>
          {#if throughputBuckets.length}
            <div aria-hidden="true">
              <Chart
                type="line"
                series={throughputSeries}
                categories={throughputCategories}
                maxXLabels={6}
                minHeight={180}
                maxHeight={240}
              />
            </div>
            <details class="data-details">
              <summary>{t('analytics.dataTable.show')}</summary>
              <div class="table-scroll">
                <table class="analytics-table compact">
                  <caption class="sr-only">{t('analytics.throughput.title')}</caption>
                  <thead>
                    <tr>
                      <th>{t('analytics.throughput.period')}</th>
                      <th class="number-cell">{t('analytics.throughput.created')}</th>
                      <th class="number-cell">{t('analytics.throughput.completed')}</th>
                      <th class="number-cell">{t('analytics.throughput.net')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each throughputBuckets as bucket (bucket.start_date)}
                      <tr>
                        <td>{period(bucket)}</td>
                        <td class="number-cell">{bucket.created}</td>
                        <td class="number-cell">{bucket.completed}</td>
                        <td class="number-cell">{signed(bucket.net_change)}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </details>
            <div class="definition-copy">
              <Text variant="subtle" size="xs">
                {t('analytics.throughput.definition')}
              </Text>
            </div>
          {/if}
        </Card>

        <Card variant="raised" padding="default">
          {#snippet header()}
            <div>
              <h3>{t('analytics.aging.title')}</h3>
              <Text variant="subtle" size="xs">{t('analytics.aging.description')}</Text>
            </div>
          {/snippet}
          {#if aging?.total_items}
            <div class="summary-strip">
              <div>
                <span>{t('analytics.aging.total')}</span>
                <strong>{aging.total_items}</strong>
              </div>
              <div>
                <span>{t('analytics.aging.median')}</span>
                <strong>{days(aging.median_days)}</strong>
              </div>
              <div>
                <span>{t('analytics.aging.p85')}</span>
                <strong>{days(aging.p85_days)}</strong>
              </div>
            </div>
            <div aria-hidden="true">
              <Chart
                type="bar"
                series={agingSeries}
                categories={agingCategories}
                maxXLabels={5}
                minHeight={180}
                maxHeight={240}
              />
            </div>
            <details class="data-details">
              <summary>{t('analytics.dataTable.show')}</summary>
              <div class="table-scroll">
                <table class="analytics-table compact">
                  <caption class="sr-only">{t('analytics.aging.title')}</caption>
                  <thead>
                    <tr>
                      <th>{t('analytics.aging.ageBand')}</th>
                      <th class="number-cell">{t('analytics.aging.itemCount')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each agingBuckets as bucket (bucket.key)}
                      <tr>
                        <td>{t(`analytics.aging.buckets.${bucket.key}`)}</td>
                        <td class="number-cell">{bucket.item_count}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </details>
          {:else}
            <div class="empty-copy">{t('analytics.aging.noActive')}</div>
          {/if}
        </Card>
      </section>

      {#if aging?.total_items}
        <section class="analytics-section aging-detail-grid" aria-label={t('analytics.aging.byStatus')}>
          <Card variant="raised" padding="none">
            {#snippet header()}
              <h3>{t('analytics.aging.byStatus')}</h3>
            {/snippet}
            <div class="table-scroll">
              <table class="analytics-table">
                <thead>
                  <tr>
                    <th>{t('analytics.aging.status')}</th>
                    <th class="number-cell">{t('analytics.aging.itemCount')}</th>
                    <th class="number-cell">{t('analytics.aging.median')}</th>
                    <th class="number-cell">{t('analytics.aging.p85')}</th>
                  </tr>
                </thead>
                <tbody>
                  {#each aging.by_status as row (row.status)}
                    <tr>
                      <td>{row.status || '—'}</td>
                      <td class="number-cell">{row.item_count}</td>
                      <td class="number-cell">{days(row.median_days)}</td>
                      <td class="number-cell">{days(row.p85_days)}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </Card>

          <Card variant="raised" padding="none">
            {#snippet header()}
              <h3>{t('analytics.aging.oldest')}</h3>
            {/snippet}
            <div class="table-scroll">
              <table class="analytics-table">
                <thead>
                  <tr>
                    <th>{t('analytics.health.item')}</th>
                    <th>{t('analytics.health.status')}</th>
                    <th class="number-cell">{t('analytics.health.age')}</th>
                  </tr>
                </thead>
                <tbody>
                  {#each aging.oldest_items as item (item.id)}
                    <tr>
                      <td>
                        <button type="button" class="item-link" onclick={() => openItem(item)}>
                          <span class="item-number">#{item.workspace_item_number}</span>
                          {item.title}
                        </button>
                      </td>
                      <td>{item.status || '—'}</td>
                      <td class="number-cell">{days(item.age_days)}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </Card>
        </section>
      {/if}

      <section class="analytics-section" aria-labelledby="delivery-heading">
        <Card variant="raised" padding="default">
          {#snippet header()}
            <div>
              <h3 id="delivery-heading">{t('analytics.deliveryTime.title')}</h3>
              <Text variant="subtle" size="xs">{t('analytics.deliveryTime.description')}</Text>
            </div>
          {/snippet}

          {#if deliveryTime?.total_items_analyzed}
            <div class="summary-strip summary-strip-wide">
              <div>
                <span>{t('analytics.deliveryTime.analyzed')}</span>
                <strong>{deliveryTime.total_items_analyzed}</strong>
              </div>
              <div>
                <span>{t('analytics.deliveryTime.average')}</span>
                <strong>{days(deliveryTime.average_days)}</strong>
              </div>
              <div>
                <span>{t('analytics.deliveryTime.median')}</span>
                <strong>{days(deliveryTime.median_days)}</strong>
              </div>
              <div>
                <span>{t('analytics.deliveryTime.p85')}</span>
                <strong>{days(deliveryTime.p85_days)}</strong>
              </div>
            </div>

            {#if !deliveryTime.data_quality?.sufficient}
              <div class="notice notice-warning mb-4" role="status">
                {t(`analytics.insufficientData.${deliveryTime.data_quality.reason}`)}
              </div>
            {/if}

            <div aria-hidden="true">
              <Chart
                type="line"
                series={deliverySeries}
                categories={deliveryCategories}
                valueFormat={days}
                yAxisFormat={formatDayNumber}
                maxXLabels={8}
                minHeight={200}
                maxHeight={280}
              />
            </div>

            <details class="data-details">
              <summary>{t('analytics.dataTable.show')}</summary>
              <div class="table-scroll">
                <table class="analytics-table compact">
                  <caption class="sr-only">{t('analytics.deliveryTime.title')}</caption>
                  <thead>
                    <tr>
                      <th>{t('analytics.deliveryTime.period')}</th>
                      <th class="number-cell">{t('analytics.deliveryTime.completed')}</th>
                      <th class="number-cell">{t('analytics.deliveryTime.median')}</th>
                      <th class="number-cell">{t('analytics.deliveryTime.p85')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each deliveryTrend as point (point.start_date)}
                      <tr>
                        <td>{period(point)}</td>
                        <td class="number-cell">{point.completed_items}</td>
                        <td class="number-cell">
                          {point.completed_items ? days(point.median_days) : '—'}
                        </td>
                        <td class="number-cell">
                          {point.completed_items ? days(point.p85_days) : '—'}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </details>

            <div class="mt-5">
              <h4>{t('analytics.deliveryTime.slowest')}</h4>
              <div class="table-scroll mt-2">
                <table class="analytics-table">
                  <thead>
                    <tr>
                      <th>{t('analytics.health.item')}</th>
                      <th>{t('analytics.deliveryTime.completedDate')}</th>
                      <th class="number-cell">{t('analytics.deliveryTime.duration')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each deliveryTime.slowest_items as item (item.id)}
                      <tr>
                        <td>
                          <button type="button" class="item-link" onclick={() => openItem(item)}>
                            <span class="item-number">#{item.workspace_item_number}</span>
                            {item.title}
                          </button>
                        </td>
                        <td>{formatDateOnly(item.completed_date)}</td>
                        <td class="number-cell">{days(item.delivery_days)}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
            <div class="definition-copy">
              <Text variant="subtle" size="xs">
                {t('analytics.deliveryTime.definition')}
              </Text>
            </div>
          {:else}
            <div class="empty-copy">
              {t(
                `analytics.insufficientData.${deliveryTime?.data_quality?.reason ||
                  'no_completed_items'}`,
              )}
            </div>
          {/if}

          {#if deliveryTime?.missing_history_items > 0}
            <div class="notice notice-warning mt-4" role="status">
              {t('analytics.deliveryTime.missingHistory', {
                count: deliveryTime.missing_history_items,
              })}
            </div>
          {/if}
        </Card>
      </section>
    {/if}
  </div>
</div>

<style>
  .analytics-filters {
    display: flex;
    align-items: end;
    justify-content: flex-end;
    gap: 0.75rem;
    flex-wrap: wrap;
  }

  .filter-field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .collection-filter {
    width: 12rem;
  }

  .preset-filter {
    width: 10rem;
  }

  .date-filter {
    width: 9.5rem;
  }

  .scope-banner,
  .notice {
    border: 1px solid var(--ds-border);
    border-radius: 0.75rem;
    background: var(--ds-background-neutral);
  }

  .scope-banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1.5rem;
    padding: 1rem;
  }

  .scope-note {
    max-width: 34rem;
  }

  .notice {
    padding: 0.75rem 1rem;
    font-size: 0.875rem;
    color: var(--ds-text-subtle);
  }

  .notice-warning {
    border-color: color-mix(in srgb, var(--ds-warning, #f59e0b) 38%, var(--ds-border));
    background: color-mix(in srgb, var(--ds-warning, #f59e0b) 8%, var(--ds-surface));
  }

  .notice-error,
  :global(.error-card) {
    border-color: color-mix(in srgb, var(--ds-danger, #ef4444) 38%, var(--ds-border));
    background: color-mix(in srgb, var(--ds-danger, #ef4444) 7%, var(--ds-surface));
  }

  .analytics-section {
    margin-bottom: 1.5rem;
  }

  .section-heading {
    display: flex;
    align-items: end;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 0.875rem;
  }

  h2 {
    margin: 0;
    color: var(--ds-text);
    font-size: 1.125rem;
    font-weight: 650;
  }

  h3,
  h4 {
    margin: 0;
    color: var(--ds-text);
    font-size: 0.875rem;
    font-weight: 650;
  }

  .metric-grid {
    display: grid;
    grid-template-columns: repeat(6, minmax(0, 1fr));
    gap: 0.75rem;
    margin-bottom: 1rem;
  }

  .metric-card {
    padding: 1rem;
    border: 1px solid var(--ds-border);
    border-radius: 0.75rem;
    background: var(--ds-surface-raised);
  }

  .metric-value {
    color: var(--ds-text);
    font-size: 1.75rem;
    font-weight: 700;
    line-height: 1.1;
  }

  .metric-label {
    margin-top: 0.35rem;
    color: var(--ds-text-subtle);
    font-size: 0.75rem;
  }

  .flow-grid,
  .aging-detail-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 1.5rem;
  }

  .summary-strip {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 0.5rem;
    margin-bottom: 1rem;
  }

  .summary-strip-wide {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }

  .summary-strip > div {
    padding: 0.75rem;
    border-radius: 0.5rem;
    background: var(--ds-background-neutral);
  }

  .summary-strip span {
    display: block;
    color: var(--ds-text-subtle);
    font-size: 0.7rem;
  }

  .summary-strip strong {
    display: block;
    margin-top: 0.2rem;
    color: var(--ds-text);
    font-size: 1.15rem;
  }

  .table-scroll {
    overflow-x: auto;
  }

  .analytics-table {
    width: 100%;
    border-collapse: collapse;
    color: var(--ds-text);
    font-size: 0.8rem;
  }

  .analytics-table th,
  .analytics-table td {
    padding: 0.75rem 1rem;
    border-bottom: 1px solid var(--ds-border);
    text-align: start;
    vertical-align: middle;
  }

  .analytics-table th {
    color: var(--ds-text-subtle);
    background: var(--ds-surface);
    font-size: 0.7rem;
    font-weight: 650;
    letter-spacing: 0.025em;
  }

  .analytics-table tbody tr:last-child td {
    border-bottom: 0;
  }

  .analytics-table.compact th,
  .analytics-table.compact td {
    padding: 0.55rem 0.75rem;
  }

  .analytics-table .number-cell {
    text-align: right;
    white-space: nowrap;
  }

  .item-link {
    display: inline-flex;
    align-items: baseline;
    gap: 0.45rem;
    max-width: 30rem;
    padding: 0;
    border: 0;
    background: transparent;
    color: var(--ds-text-link);
    cursor: pointer;
    font: inherit;
    text-align: start;
  }

  .item-link:hover {
    text-decoration: underline;
  }

  .item-link:focus-visible {
    border-radius: 0.2rem;
    outline: 2px solid var(--ds-border-focused);
    outline-offset: 3px;
  }

  .item-number {
    color: var(--ds-text-subtlest);
    font-size: 0.7rem;
    white-space: nowrap;
  }

  .flag-list {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
  }

  .flag {
    padding: 0.15rem 0.4rem;
    border-radius: 999px;
    background: var(--ds-background-neutral);
    color: var(--ds-text-subtle);
    font-size: 0.65rem;
    white-space: nowrap;
  }

  .empty-copy {
    padding: 2.5rem 1rem;
    color: var(--ds-text-subtle);
    font-size: 0.875rem;
    text-align: center;
  }

  .data-details {
    margin-top: 0.5rem;
    border-top: 1px solid var(--ds-border);
  }

  .data-details summary {
    padding: 0.75rem 0;
    color: var(--ds-text-link);
    font-size: 0.75rem;
    cursor: pointer;
  }

  .definition-copy {
    display: block;
    margin-top: 0.75rem;
  }

  @media (max-width: 1100px) {
    .metric-grid {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }

    .flow-grid,
    .aging-detail-grid {
      grid-template-columns: 1fr;
    }
  }

  @media (max-width: 720px) {
    .analytics-filters {
      width: 100%;
      justify-content: stretch;
    }

    .filter-field,
    .collection-filter,
    .preset-filter,
    .date-filter {
      width: calc(50% - 0.375rem);
    }

    .scope-banner,
    .section-heading {
      align-items: flex-start;
      flex-direction: column;
    }

    .metric-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    .summary-strip,
    .summary-strip-wide {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
</style>
