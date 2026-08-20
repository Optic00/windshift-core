<script>
  import { currentRoute } from '../router.js';
  import TabNav from '../components/TabNav.svelte';
  import LLMConnectionManager from './LLMConnectionManager.svelte';
  import AIFeaturesSettings from './AIFeaturesSettings.svelte';
  import AgentTemplateManager from './AgentTemplateManager.svelte';
  import WorkItemStalenessSettings from './WorkItemStalenessSettings.svelte';
  import { t } from '../stores/i18n.svelte.js';

  let subtab = $derived($currentRoute.query?.subtab || 'connections');

  let tabs = $derived([
    { id: 'connections', label: t('settings.adminItems.llmConnections.title') },
    { id: 'features', label: t('settings.adminItems.aiFeatures.title') },
    { id: 'agent-templates', label: t('settings.adminItems.agentTemplates.title') },
    { id: 'staleness', label: t('settings.adminItems.workItemStaleness.title') },
  ]);
</script>

<div class="space-y-6">
  <TabNav {tabs} basePath="/admin/llm-connections" defaultTab="connections" />

  <div>
    {#if subtab === 'features'}
      <AIFeaturesSettings />
    {:else if subtab === 'agent-templates'}
      <AgentTemplateManager />
    {:else if subtab === 'staleness'}
      <WorkItemStalenessSettings />
    {:else}
      <LLMConnectionManager />
    {/if}
  </div>
</div>
