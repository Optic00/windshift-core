<script>
  import { onMount } from 'svelte';
  import {
    AlertCircle,
    Clock3,
    Gauge,
    Info,
    RefreshCw,
    Ruler,
    UserRoundMinus,
  } from '@lucide/svelte';
  import { api } from '../../api.js';
  import { navigate } from '../../router.js';
  import { t } from '../../stores/i18n.svelte.js';
  import AlertBox from '../../components/AlertBox.svelte';
  import Badge from '../../components/Badge.svelte';
  import Button from '../../components/Button.svelte';
  import Card from '../../components/Card.svelte';
  import Input from '../../components/Input.svelte';
  import Label from '../../components/Label.svelte';
  import Select from '../../components/Select.svelte';
  import StateDisplay from '../../components/StateDisplay.svelte';
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

  function flagVariant(flag) {
    if (flag === 'overdue') return 'danger';
    if (flag === 'stale') return 'warning';
    return 'neutral';
  }

  const dataset = $derived(analyticsData?.dataset || null);
  const health = $derived(analyticsData?.health || null);
  const throughput = $derived(analyticsData?.throughput || null);
  const aging = $derived(analyticsData?.aging_wip || null);
  const deliveryTime = $derived(analyticsData?.delivery_time || null);

  const healthMetrics = $derived.by(() => [
    {
      key: 'unfinished',
      icon: Clock3,
      iconColor: 'var(--ds-icon-subtle)',
      label: t('analytics.health.unfinished'),
      value: health?.unfinished_items || 0,
    },
    {
      key: 'overdue',
      icon: AlertCircle,
      iconColor: 'var(--ds-icon-danger)',
      label: t('analytics.health.overdue'),
      value: health?.overdue || 0,
    },
    {
      key: 'stale',
      icon: Gauge,
      iconColor: 'var(--ds-icon-warning)',
      label: t('analytics.health.stale'),
      value: health?.stale || 0,
    },
    {
      key: 'unassigned',
      icon: UserRoundMinus,
      iconColor: 'var(--ds-icon-subtle)',
      label: t('analytics.health.unassigned'),
      value: health?.unassigned || 0,
    },
    {
      key: 'without-priority',
      icon: AlertCircle,
      iconColor: 'var(--ds-icon-subtle)',
      label: t('analytics.health.withoutPriority'),
      value: health?.without_priority || 0,
    },
    {
      key: 'without-estimate',
      icon: Ruler,
      iconColor: 'var(--ds-icon-subtle)',
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
      color: 'var(--ds-icon-accent-blue)',
      values: throughputBuckets.map((bucket) => bucket.created),
      showArea: false,
    },
    {
      key: 'completed',
      label: t('analytics.throughput.completed'),
      color: 'var(--ds-icon-accent-green)',
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
      color: 'var(--ds-icon-accent-orange)',
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
      color: 'var(--ds-icon-accent-blue)',
      values: deliveryChartTrend.map((point) => point.median_days),
      showArea: false,
    },
    {
      key: 'p85',
      label: t('analytics.deliveryTime.p85'),
      color: 'var(--ds-icon-accent-teal)',
      values: deliveryChartTrend.map((point) => point.p85_days),
      dashed: true,
      showArea: false,
    },
  ]);
</script>

<div class="analytics-page min-h-screen" style="background-color: var(--ds-surface);">
  <div class="analytics-shell">
    <PageHeader title={t('analytics.title')} subtitle={t('analytics.subtitle')} />

    <div class="filter-card">
      <Card variant="raised" padding="default" rounded="lg">
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
      </Card>
    </div>

    {#if collectionLoadError}
      <div role="status">
        <AlertBox variant="warning" class="mb-4">
          {#snippet children()}{t('analytics.collectionLoadError')}{/snippet}
        </AlertBox>
      </div>
    {/if}

    {#if validationCode}
      <div role="alert">
        <AlertBox variant="error" class="mb-4">
          {#snippet children()}{t(`analytics.validation.${validationCode}`)}{/snippet}
        </AlertBox>
      </div>
    {:else if loadError}
      <div role="alert">
        <AlertBox variant="error" class="mb-5">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div>
              <div class="font-semibold" style="color: var(--ds-text);">
                {t('analytics.errorTitle')}
              </div>
              <Text variant="subtle" size="sm">{loadError}</Text>
            </div>
            <Button variant="default" size="small" icon={RefreshCw} onclick={retry}>
              {t('analytics.retry')}
            </Button>
          </div>
        </AlertBox>
      </div>
    {:else if loading}
      <div role="status">
        <StateDisplay type="loading" message={t('analytics.loading')} class="py-20" />
      </div>
    {:else if analyticsData}
      {#if dataset}
        <div class="scope-banner mb-6">
          <Info class="scope-icon" aria-hidden="true" />
          <div class="scope-summary">
            <div class="scope-title">
              {dataset.cohort_mode === 'current_collection'
                ? t('analytics.scope.currentCollection')
                : t('analytics.scope.currentWorkspace')}
            </div>
            <Text variant="subtle" size="xs">
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

      <section class="analytics-section" aria-label={t('analytics.health.title')}>
        <div class="section-heading">
          <div>
            <h2>{t('analytics.health.title')}</h2>
            <Text variant="subtle" size="sm">{t('analytics.health.description')}</Text>
          </div>
          {#if health?.stale_after_days}
            <Text variant="subtle" size="xs">
              {t('analytics.health.staleHint', { days: health.stale_after_days })}
            </Text>
          {/if}
        </div>
        <Card variant="raised" padding="none" rounded="lg">
          <div class="widget-intro">
            <div class="metric-grid">
              {#each healthMetrics as metric (metric.key)}
                {@const MetricIcon = metric.icon}
                <div class="health-metric">
                  <MetricIcon class="metric-icon" style="color: {metric.iconColor};" />
                  <div>
                    <div class="metric-label">{metric.label}</div>
                    <div class="metric-value">{metric.value}</div>
                  </div>
                </div>
              {/each}
            </div>
          </div>
        </Card>

        <Card variant="raised" padding="none" rounded="lg">
          <div class="panel-titlebar">
            <h3>{t('analytics.health.attentionItems')}</h3>
          </div>
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
                            <Badge
                              variant={flagVariant(flag)}
                              size="xs"
                              class="signal-badge signal-{flag}"
                            >
                              {t(`analytics.health.flags.${flag}`)}
                            </Badge>
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
        <article class="analytics-panel">
          <div class="section-heading compact">
            <div>
              <h2>{t('analytics.throughput.title')}</h2>
              <Text variant="subtle" size="sm">{t('analytics.throughput.description')}</Text>
            </div>
          </div>
          <Card variant="raised" padding="default" rounded="lg">
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
        </article>

        <article class="analytics-panel">
          <div class="section-heading compact">
            <div>
              <h2>{t('analytics.aging.title')}</h2>
              <Text variant="subtle" size="sm">{t('analytics.aging.description')}</Text>
            </div>
          </div>
          <Card variant="raised" padding="default" rounded="lg">
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
        </article>
      </section>

      {#if aging?.total_items}
        <section class="analytics-section aging-detail-grid" aria-label={t('analytics.aging.byStatus')}>
          <article class="analytics-panel">
            <div class="section-heading compact">
              <h2>{t('analytics.aging.byStatus')}</h2>
            </div>
            <Card variant="raised" padding="none" rounded="lg">
              <div class="table-scroll table-scroll-flush">
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
          </article>

          <article class="analytics-panel">
            <div class="section-heading compact">
              <h2>{t('analytics.aging.oldest')}</h2>
            </div>
            <Card variant="raised" padding="none" rounded="lg">
              <div class="table-scroll table-scroll-flush">
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
          </article>
        </section>
      {/if}

      <section class="analytics-section" aria-label={t('analytics.deliveryTime.title')}>
        <div class="section-heading">
          <div>
            <h2>{t('analytics.deliveryTime.title')}</h2>
            <Text variant="subtle" size="sm">{t('analytics.deliveryTime.description')}</Text>
          </div>
        </div>
        <Card variant="raised" padding="default" rounded="lg">

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
              <div role="status">
                <AlertBox variant="warning" class="mb-4">
                  {#snippet children()}
                    {t(`analytics.insufficientData.${deliveryTime.data_quality.reason}`)}
                  {/snippet}
                </AlertBox>
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
            <div role="status">
              <AlertBox variant="warning" class="mt-4">
                {#snippet children()}
                  {t('analytics.deliveryTime.missingHistory', {
                    count: deliveryTime.missing_history_items,
                  })}
                {/snippet}
              </AlertBox>
            </div>
          {/if}
        </Card>
      </section>
    {/if}
  </div>
</div>

<style>
  .analytics-shell {
    width: min(100%, 96rem);
    margin: 0 auto;
    padding: 1.5rem;
  }

  .filter-card {
    margin-bottom: 1rem;
  }

  .analytics-filters {
    display: flex;
    align-items: end;
    gap: 1rem;
    flex-wrap: wrap;
  }

  .filter-field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .collection-filter {
    width: min(20rem, 100%);
  }

  .preset-filter {
    width: 12rem;
  }

  .date-filter {
    width: 10rem;
  }

  .scope-banner {
    display: grid;
    grid-template-columns: auto max-content minmax(18rem, 1fr);
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 0;
    border-top: 1px solid var(--ds-border);
    border-bottom: 1px solid var(--ds-border);
  }

  .analytics-page :global(.scope-icon) {
    width: 1rem;
    height: 1rem;
    color: var(--ds-icon-subtle);
  }

  .scope-summary {
    min-width: 0;
  }

  .scope-title {
    color: var(--ds-text);
    font-size: 0.75rem;
    font-weight: 600;
  }

  .scope-note {
    min-width: 0;
    padding-left: 0.75rem;
    border-left: 1px solid var(--ds-border);
  }

  .analytics-section {
    margin-bottom: 2rem;
  }

  .analytics-section:not(.flow-grid):not(.aging-detail-grid) {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .section-heading {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 1rem;
  }

  .section-heading.compact {
    min-height: 3.25rem;
    margin-bottom: 0.75rem;
  }

  .section-heading h2,
  .panel-titlebar h3,
  h4 {
    margin: 0;
    color: var(--ds-text);
    font-size: 0.875rem;
    font-weight: 600;
  }

  .section-heading h2 {
    margin-bottom: 0.125rem;
    font-size: 1rem;
  }

  h4 {
    font-size: 0.8125rem;
  }

  .widget-intro {
    padding: 1.25rem;
  }

  .panel-titlebar {
    padding: 0.875rem 1rem;
    border-bottom: 1px solid var(--ds-border);
  }

  .metric-grid {
    display: grid;
    grid-template-columns: repeat(6, minmax(0, 1fr));
  }

  .health-metric {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    min-width: 0;
    padding: 0.125rem 1rem;
    border-right: 1px solid var(--ds-border);
  }

  .health-metric:first-child {
    padding-left: 0;
  }

  .health-metric:last-child {
    padding-right: 0;
    border-right: 0;
  }

  .analytics-page :global(.metric-icon) {
    width: 1rem;
    height: 1rem;
    flex: 0 0 auto;
  }

  .metric-label {
    overflow: hidden;
    color: var(--ds-text-subtle);
    font-size: 0.75rem;
    line-height: 1.25;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .metric-value {
    margin-top: 0.125rem;
    color: var(--ds-text);
    font-size: 1.375rem;
    font-weight: 600;
    line-height: 1.2;
  }

  .flow-grid,
  .aging-detail-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 1.25rem;
  }

  .analytics-panel {
    min-width: 0;
  }

  .summary-strip {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    margin-bottom: 0.5rem;
    padding-bottom: 1rem;
    border-bottom: 1px solid var(--ds-border);
  }

  .summary-strip-wide {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }

  .summary-strip > div {
    min-width: 0;
    padding: 0 1rem;
    border-right: 1px solid var(--ds-border);
  }

  .summary-strip > div:first-child {
    padding-left: 0;
  }

  .summary-strip > div:last-child {
    padding-right: 0;
    border-right: 0;
  }

  .summary-strip span {
    display: block;
    color: var(--ds-text-subtle);
    font-size: 0.75rem;
    line-height: 1.25;
  }

  .summary-strip strong {
    display: block;
    margin-top: 0.25rem;
    color: var(--ds-text);
    font-size: 1.25rem;
    font-weight: 600;
    line-height: 1.25;
  }

  .table-scroll {
    overflow-x: auto;
    border: 1px solid var(--ds-border);
    border-radius: 0.375rem;
  }

  .panel-titlebar + .table-scroll,
  .table-scroll-flush {
    border: 0;
    border-radius: 0;
  }

  .analytics-table {
    width: 100%;
    border-collapse: collapse;
    color: var(--ds-text);
    font-size: 0.8125rem;
  }

  .analytics-table th,
  .analytics-table td {
    padding: 0.75rem 1rem;
    border-bottom: 1px solid var(--ds-border);
    text-align: start;
    vertical-align: middle;
  }

  .analytics-table th {
    background: var(--ds-surface);
    color: var(--ds-text);
    font-size: 0.75rem;
    font-weight: 600;
  }

  .analytics-table tbody tr:last-child td {
    border-bottom: 0;
  }

  .analytics-table tbody tr:hover {
    background: var(--ds-background-neutral-hovered);
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
    color: var(--ds-text);
    cursor: pointer;
    font: inherit;
    font-weight: 500;
    text-align: start;
  }

  .item-link:hover {
    color: var(--ds-text-link);
    text-decoration: underline;
  }

  .item-link:focus-visible {
    border-radius: 0.2rem;
    outline: 2px solid var(--ds-border-focused);
    outline-offset: 3px;
  }

  .item-number {
    color: var(--ds-text-subtlest);
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 0.6875rem;
    white-space: nowrap;
  }

  .flag-list {
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
  }

  .flag-list :global(span) {
    white-space: nowrap;
  }

  .analytics-page :global(.signal-overdue) {
    background-color: var(--ds-danger-subtle);
    color: var(--ds-text-danger);
  }

  .analytics-page :global(.signal-stale) {
    background-color: var(--ds-warning-subtle);
    color: var(--ds-text-warning);
  }

  .empty-copy {
    padding: 2.5rem 1rem;
    color: var(--ds-text-subtle);
    font-size: 0.875rem;
    text-align: center;
  }

  .data-details {
    margin-top: 0.25rem;
    border-top: 1px solid var(--ds-border);
  }

  .data-details summary {
    padding: 0.75rem 0;
    color: var(--ds-text-link);
    font-size: 0.75rem;
    font-weight: 500;
    cursor: pointer;
  }

  .data-details summary:hover {
    color: var(--ds-text-link-hovered);
  }

  .definition-copy {
    display: block;
    margin-top: 0.75rem;
    padding-top: 0.75rem;
    border-top: 1px solid var(--ds-border);
  }

  @media (max-width: 1100px) {
    .metric-grid {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }

    .flow-grid,
    .aging-detail-grid {
      grid-template-columns: 1fr;
    }

    .health-metric:nth-child(3) {
      border-right: 0;
    }

    .health-metric:nth-child(n + 4) {
      margin-top: 0.875rem;
      padding-top: 0.875rem;
      border-top: 1px solid var(--ds-border);
    }

    .health-metric:nth-child(4) {
      padding-left: 0;
    }
  }

  @media (max-width: 720px) {
    .analytics-shell {
      padding: 1rem;
    }

    .analytics-filters {
      width: 100%;
    }

    .collection-filter {
      width: 100%;
    }

    .preset-filter,
    .date-filter {
      width: calc((100% - 2rem) / 3);
    }

    .scope-banner,
    .section-heading {
      align-items: flex-start;
    }

    .scope-banner {
      grid-template-columns: auto minmax(0, 1fr);
    }

    .scope-note {
      grid-column: 1 / -1;
      padding-top: 0.75rem;
      padding-left: 0;
      border-top: 1px solid var(--ds-border);
      border-left: 0;
    }

    .metric-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    .health-metric {
      margin-top: 0.75rem;
      padding: 0.75rem 0 0;
      border-top: 1px solid var(--ds-border);
      border-right: 0;
    }

    .health-metric:nth-child(-n + 2) {
      margin-top: 0;
      padding-top: 0;
      border-top: 0;
    }

    .health-metric:nth-child(odd) {
      padding-right: 0.75rem;
      border-right: 1px solid var(--ds-border);
    }

    .health-metric:nth-child(even) {
      padding-left: 0.75rem;
    }

    .summary-strip-wide {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    .summary-strip > div {
      padding: 0 0.75rem;
    }

    .summary-strip-wide > div:nth-child(2) {
      border-right: 0;
    }

    .summary-strip-wide > div:nth-child(n + 3) {
      margin-top: 0.75rem;
      padding-top: 0.75rem;
      border-top: 1px solid var(--ds-border);
    }
  }

  @media (max-width: 520px) {
    .preset-filter {
      width: 100%;
    }

    .date-filter {
      width: calc(50% - 0.5rem);
    }

    .section-heading {
      flex-direction: column;
    }

    .summary-strip {
      gap: 0.75rem;
    }

    .summary-strip > div {
      padding: 0;
      border-right: 0;
    }
  }
</style>
