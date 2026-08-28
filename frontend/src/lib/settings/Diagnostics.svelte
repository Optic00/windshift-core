<script>
  import { currentRoute } from '../router.js';
  import PermissionGuard from '../layout/PermissionGuard.svelte';
  import SectionHeader from '../layout/SectionHeader.svelte';
  import TabNav from '../components/TabNav.svelte';
  import ServerClockSection from './diagnostics/ServerClockSection.svelte';
  import ActionLogsSection from './diagnostics/ActionLogsSection.svelte';
  import WebhookDeliveriesSection from './diagnostics/WebhookDeliveriesSection.svelte';
  import SchedulerRunsSection from './diagnostics/SchedulerRunsSection.svelte';
  import FracIndexSection from './diagnostics/FracIndexSection.svelte';
  import LLMHealthSection from './diagnostics/LLMHealthSection.svelte';
  import RunnerPoolsSection from './diagnostics/RunnerPoolsSection.svelte';
  import DatabasePoolsSection from './diagnostics/DatabasePoolsSection.svelte';
  import CacheMemorySection from './diagnostics/CacheMemorySection.svelte';
  import RecurrenceVolumeSection from './diagnostics/RecurrenceVolumeSection.svelte';
  import DomainEventsSection from './diagnostics/DomainEventsSection.svelte';
  import SCMHealthSection from './diagnostics/SCMHealthSection.svelte';

  const diagnosticGroups = [
    { label: 'Overview', tabs: [{ id: 'clock', label: 'Server clock' }] },
    {
      label: 'Automation',
      tabs: [
        { id: 'actions', label: 'Action executions' },
        { id: 'webhooks', label: 'Webhook deliveries' },
        { id: 'schedulers', label: 'Background jobs' },
        { id: 'recurrence-volume', label: 'Recurrence' },
        { id: 'domain-events', label: 'Domain events' },
      ],
    },
    { label: 'Data', tabs: [{ id: 'frac-index', label: 'Frac index' }] },
    { label: 'AI / LLM', tabs: [{ id: 'llm-health', label: 'AI / LLM' }] },
    {
      label: 'Infrastructure',
      tabs: [
        { id: 'runner-pools', label: 'Runner pools' },
        { id: 'database-pools', label: 'Database pools' },
        { id: 'cache-memory', label: 'Cache memory' },
      ],
    },
    { label: 'SCM', tabs: [{ id: 'scm-health', label: 'SCM connections' }] },
  ];

  const tabs = diagnosticGroups.map((group) => ({
    id: group.tabs[0].id,
    label: group.label,
    matches: group.tabs.map((tab) => tab.id),
  }));

  const subtab = $derived($currentRoute.query?.subtab || 'clock');
  const activeGroup = $derived(
    diagnosticGroups.find((group) => group.tabs.some((tab) => tab.id === subtab)) ?? diagnosticGroups[0]
  );
</script>

<PermissionGuard requireSystemAdmin={true}>
  <div class="space-y-6" data-testid="diagnostics-page">
    <SectionHeader
      title="Diagnostics"
      subtitle="Operational signals from the running system. Diagnostic warning thresholds can be adjusted without changing hard safety limits."
    />

    <TabNav {tabs} basePath="/admin/diagnostics" defaultTab="clock" />

    {#if activeGroup.tabs.length > 1}
      <TabNav tabs={activeGroup.tabs} basePath="/admin/diagnostics" defaultTab={activeGroup.tabs[0].id} />
    {/if}

    <div>
      {#if subtab === 'clock'}
        <ServerClockSection />
      {:else if subtab === 'actions'}
        <ActionLogsSection />
      {:else if subtab === 'webhooks'}
        <WebhookDeliveriesSection />
      {:else if subtab === 'schedulers'}
        <SchedulerRunsSection />
      {:else if subtab === 'frac-index'}
        <FracIndexSection />
      {:else if subtab === 'llm-health'}
        <LLMHealthSection />
      {:else if subtab === 'runner-pools'}
        <RunnerPoolsSection />
      {:else if subtab === 'database-pools'}
        <DatabasePoolsSection />
      {:else if subtab === 'cache-memory'}
        <CacheMemorySection />
      {:else if subtab === 'recurrence-volume'}
        <RecurrenceVolumeSection />
      {:else if subtab === 'domain-events'}
        <DomainEventsSection />
      {:else if subtab === 'scm-health'}
        <SCMHealthSection />
      {/if}
    </div>
  </div>
</PermissionGuard>
