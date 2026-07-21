/**
 * Analytics translations for English locale.
 */
export default {
  analytics: {
    title: 'Analytics',
    subtitle: 'Delivery health and flow, without requiring iterations',
    loading: 'Loading analytics...',
    noData: 'No data available',
    errorTitle: 'Analytics could not be loaded',
    unsupportedVersion:
      'The server returned an unsupported analytics format. Refresh after the deployment finishes.',
    collectionLoadError:
      'Collections could not be loaded. Analytics is showing all workspace items.',
    retry: 'Retry',
    dateRange: 'Date range',
    collection: 'Collection',
    allItems: 'All workspace items',
    from: 'From',
    to: 'To',
    daysValue: '{value}d',
    items_one: '{count} item',
    items_other: '{count} items',
    range: {
      last30Days: 'Last 30 days',
      last12Weeks: 'Last 12 weeks',
      last6Months: 'Last 6 months',
      lastYear: 'Last year',
      custom: 'Custom',
    },
    validation: {
      invalid: 'Enter valid start and end dates.',
      reversed: 'The start date must be on or before the end date.',
      too_long: 'Choose a date range of 366 days or less.',
    },
    scope: {
      summary: '{items} current items · {from}–{to}',
      currentWorkspace: 'Current workspace cohort',
      currentWorkspaceNote:
        'The date range applies to flow and delivery charts; health and aging are current snapshots. Historical charts use items that are in this workspace today. Moved or deleted items are not included.',
      currentCollection: 'Current collection cohort',
      currentCollectionNote:
        'The date range applies to flow and delivery charts; health and aging are current snapshots. Historical charts use items that match this collection today. Changing the collection can change the cohort.',
    },
    health: {
      title: 'Needs attention',
      description: 'Current unfinished work with signals that deserve a closer look.',
      unfinished: 'Unfinished',
      overdue: 'Overdue',
      stale: 'Stale',
      staleHint: 'No activity for {days}+ days',
      unassigned: 'Unassigned',
      withoutPriority: 'No priority',
      withoutEstimate: 'No estimate',
      attentionItems: 'Items to review',
      item: 'Item',
      status: 'Status',
      age: 'Age',
      signals: 'Signals',
      flags: {
        overdue: 'Overdue',
        stale: 'Stale',
        unassigned: 'Unassigned',
        without_priority: 'No priority',
        without_estimate: 'No estimate',
      },
      allClear: 'No unfinished items currently match an attention signal.',
    },
    throughput: {
      title: 'Created vs completed',
      description:
        'Weekly arrivals and first completions. Reopening an item does not rewrite its original completion.',
      created: 'Created',
      completed: 'Completed',
      net: 'Net change',
      average: 'Avg completed / week',
      period: 'Period',
      definition: 'Completion means the first transition into a completed status.',
    },
    aging: {
      title: 'Aging work in progress',
      description: 'How long currently unfinished items have been open.',
      total: 'Active items',
      median: 'Median age',
      p85: '85th percentile',
      ageBand: 'Age band',
      itemCount: 'Items',
      byStatus: 'Age by status',
      oldest: 'Oldest unfinished items',
      status: 'Status',
      noActive: 'There is no unfinished work in this scope.',
      buckets: {
        '0_7': '0–7 days',
        '8_14': '8–14 days',
        '15_30': '15–30 days',
        '31_60': '31–60 days',
        '61_plus': '61+ days',
      },
    },
    deliveryTime: {
      title: 'Delivery time',
      description: 'Creation to first completion, grouped by completion week.',
      analyzed: 'Completed items',
      average: 'Average',
      median: 'Median',
      p85: '85th percentile',
      period: 'Completion period',
      completed: 'Completed',
      slowest: 'Longest delivery times',
      completedDate: 'First completed',
      duration: 'Delivery time',
      missingHistory: '{count} currently completed items were excluded because their completion history is missing.',
      missingHistory_one:
        '1 currently completed item was excluded because its completion history is missing.',
      missingHistory_other:
        '{count} currently completed items were excluded because their completion history is missing.',
      definition:
        'Measured from item creation to its first transition into a completed status. Later reopenings do not change the value.',
    },
    dataTable: {
      show: 'View data table',
    },
    insufficientData: {
      no_items: 'This scope has no items yet.',
      no_active_items: 'There is no unfinished work in this scope.',
      no_completed_items: 'No first completions were recorded in the selected date range.',
      few_completed_items:
        'Only a few items were completed in this range. Treat percentiles as directional.',
    },
  },
};
