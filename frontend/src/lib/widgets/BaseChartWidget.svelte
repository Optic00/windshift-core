<script>
  import Chart from './Chart.svelte';
  import { t } from '../stores/i18n.svelte.js';

  let {
    chartData = [],
    seriesKey = 'default',
    seriesLabel = '',
    seriesColor = '#6366f1',
    chartType = 'line',
    minHeight = 220,
    showYAxis = false,
    gridLineCount = 4,
    gridDashed = true,
    customEmptyMessage = '',
    customLabelFormatter = null
  } = $props();

  // Default date formatter
  const defaultFmtDate = (v) => {
    const d = v instanceof Date ? v : new Date(v);
    return `${String(d.getMonth() + 1).padStart(2, '0')}/${String(d.getDate()).padStart(2, '0')}`;
  };

  // Allow custom date formatter
  const fmtDate = $derived(customLabelFormatter || defaultFmtDate);

  // Extract categories and create series
  const categories = $derived(chartData.map(d => d.label || fmtDate(d.date)));
  const series = $derived([{
    key: seriesKey,
    label: seriesLabel,
    color: seriesColor,
    values: chartData.map(d => d.count ?? 0)
  }]);

  // seriesKey doubles as the i18n prefix: widgets.<seriesKey>Chart.emptyMessage
  // must exist for any new seriesKey, or pass customEmptyMessage instead.
  const emptyMessage = $derived(
    customEmptyMessage || t(`widgets.${seriesKey}Chart.emptyMessage`)
  );
</script>

<Chart
  type={chartType}
  {series}
  {categories}
  {minHeight}
  {showYAxis}
  {gridLineCount}
  {gridDashed}
  {emptyMessage}
/>