<script>
  import { onMount } from 'svelte';
  import { IconSettings, IconUsers } from '@tabler/icons-svelte-runes';
  import { currentRoute } from '../../router.js';
  import { api } from '../../api.js';
  import { t } from '../../stores/i18n.svelte.js';
  import { errorToast, successToast } from '../../stores/toasts.svelte.js';
  import { channelCategoriesStore } from '../../stores/channelCategories.js';
  import ChannelAdminShell from './ChannelAdminShell.svelte';
  import ChannelAdminSettings from './ChannelAdminSettings.svelte';
  import ChannelPortalConfig from './ChannelPortalConfig.svelte';
  import ChannelManagersTab from '../../settings/ChannelManagersTab.svelte';
  import { channelBasicFormData, parseChannelConfig, saveChannelSettings } from './channelAdmin.js';

  let channel = $state(null);
  let loading = $state(true);
  let saving = $state(false);
  let activeTab = $state('settings');

  let portalConfigRef = $state(null);

  let channelFormData = $state({
    name: '',
    description: '',
    category_id: null,
  });

  let portalFormData = $state({
    slug: '',
    workspace_ids: [],
    enabled: false,
    title: '',
    description: '',
    registration_mode: 'open',
    allowed_domains: '',
  });

  let channelId = $derived(parseInt($currentRoute.path.match(/\/admin\/channels\/(\d+)\/portal/)?.[1]));

  onMount(async () => {
    await channelCategoriesStore.init();
    await loadChannel();
  });

  async function loadChannel() {
    try {
      loading = true;
      channel = await api.channels.get(channelId);

      channelFormData = channelBasicFormData(channel);

      const config = parseChannelConfig(channel.config);
      portalFormData = {
        slug: config.portal_slug || '',
        workspace_ids: config.portal_workspace_ids || [],
        enabled: channel.status === 'enabled',
        title: config.portal_title || '',
        description: config.portal_description || '',
        registration_mode: config.portal_registration_mode === 'manual' ? 'manual' : 'open',
        allowed_domains: Array.isArray(config.portal_allowed_domains)
          ? config.portal_allowed_domains.join(', ')
          : '',
      };
    } catch (err) {
      console.error('Failed to load channel:', err);
      errorToast(t('channel.failedToLoad', 'Failed to load channel'));
    } finally {
      loading = false;
    }
  }

  async function handleSaveSettings() {
    if (!channel) return;

    if (portalConfigRef) {
      const validation = portalConfigRef.validate();
      if (!validation.valid) {
        errorToast(validation.message);
        return;
      }
    }

    try {
      saving = true;

      await saveChannelSettings({
        channel,
        channelFormData,
        configRef: portalConfigRef,
        enabled: portalFormData.enabled,
      });

      channel = await api.channels.get(channelId);
      successToast(t('common.saved'));
    } catch (err) {
      console.error('Failed to save:', err);
      errorToast(err.message || t('common.error'));
    } finally {
      saving = false;
    }
  }

  const tabs = [
    { id: 'settings', label: () => t('channel.configuration'), icon: IconSettings },
    { id: 'managers', label: () => t('channel.managers'), icon: IconUsers },
  ];
</script>

<ChannelAdminShell
  {loading}
  {channel}
  bind:activeTab
  {tabs}
  subtitle={t('channels.portal', 'Portal Channel')}
  openUrl={portalFormData.slug ? `/portal/${portalFormData.slug}` : ''}
  openLabel={t('channel.openPortal')}
>
  {#snippet children(tabId)}
    {#if tabId === 'settings'}
      <ChannelAdminSettings bind:channelFormData {saving} onSave={handleSaveSettings}>
        <ChannelPortalConfig
          bind:this={portalConfigRef}
          bind:formData={portalFormData}
        />
      </ChannelAdminSettings>
    {:else if tabId === 'managers'}
      <div class="px-16 py-8">
        <ChannelManagersTab
          channelId={channel.id}
          channelName={channel.name}
          isDefault={channel.is_default}
        />
      </div>
    {/if}
  {/snippet}
</ChannelAdminShell>
