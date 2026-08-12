<script>
  import { onMount } from 'svelte';
  import { navigate } from '../../router.js';
  import { t } from '../../stores/i18n.svelte.js';
  import MarkdownPrintView from '../print/MarkdownPrintView.svelte';
  import { readMarkdownPrintPayload } from '../print/markdownPrintWindow.js';

  let content = $state('');
  let title = $state('Time Tracking Report');
  let loading = $state(true);
  let error = $state('');

  onMount(() => {
    const payload = readMarkdownPrintPayload('time-report');
    if (payload) {
      content = payload.content;
      title = payload.title;
    } else {
      error = t('time.reports.printUnavailable');
    }
    loading = false;
  });
</script>

<MarkdownPrintView
  {content}
  {title}
  {loading}
  {error}
  loadingLabel={t('time.reports.printLoading')}
  backLabel={t('time.reports.backToReports')}
  onback={() => navigate('/time/worklogs')}
  testId="time-report-print"
/>
