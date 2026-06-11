<script>
  import BaseChartWidget from './BaseChartWidget.svelte';
  import { t } from '../stores/i18n.svelte.js';

  let { chartData = [] } = $props();
</script>

<BaseChartWidget
  bind:chartData
  seriesKey="created"
  seriesLabel={t('widgets.createdChart.title')}
  seriesColor="#3b82f6"
/>