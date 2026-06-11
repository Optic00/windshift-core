<script>
  import BaseChartWidget from './BaseChartWidget.svelte';
  import { t } from '../stores/i18n.svelte.js';

  let { chartData = [] } = $props();
</script>

<BaseChartWidget
  bind:chartData
  seriesKey="completion"
  seriesLabel={t('widgets.completionChart.title')}
  seriesColor="#10b981"
/>