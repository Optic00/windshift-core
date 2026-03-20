<script>
  import { currentRoute } from '../router.js';
  import TabNav from '../components/TabNav.svelte';
  import LLMConnectionManager from './LLMConnectionManager.svelte';
  import AIFeaturesSettings from './AIFeaturesSettings.svelte';
  import { t } from '../stores/i18n.svelte.js';

  let subtab = $derived($currentRoute.query?.subtab || 'connections');

  let tabs = $derived([
    { id: 'connections', label: t('settings.adminItems.llmConnections.title') },
    { id: 'features', label: t('settings.adminItems.aiFeatures.title') },
  ]);
</script>

<div class="space-y-6">
  <TabNav {tabs} basePath="/admin/llm-connections" defaultTab="connections" />

  <div>
    {#if subtab === 'features'}
      <AIFeaturesSettings />
    {:else}
      <LLMConnectionManager />
    {/if}
  </div>
</div>
