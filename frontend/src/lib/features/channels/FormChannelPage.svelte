<script>
  import { onMount, onDestroy } from 'svelte';
  import { IconSettings, IconForms, IconCode, IconUsers } from '@tabler/icons-svelte-runes';
  import { currentRoute, navigate } from '../../router.js';
  import { api } from '../../api.js';
  import { t } from '../../stores/i18n.svelte.js';
  import { errorToast, successToast } from '../../stores/toasts.svelte.js';
  import { channelCategoriesStore } from '../../stores/channelCategories.js';
  import { formBuilderStore } from '../../stores/formBuilderStore.svelte.js';
  import ChannelAdminShell from './ChannelAdminShell.svelte';
  import ChannelAdminSettings from './ChannelAdminSettings.svelte';
  import FormBuilder from './FormBuilder.svelte';
  import ChannelFormConfig from './ChannelFormConfig.svelte';
  import FormIntegrationPanel from './FormIntegrationPanel.svelte';
  import ChannelManagersTab from '../../settings/ChannelManagersTab.svelte';
  import CreateFormModal from './CreateFormModal.svelte';
  import { channelBasicFormData, parseChannelConfig, saveChannelSettings } from './channelAdmin.js';

  let channel = $state(null);
  let loading = $state(true);
  let saving = $state(false);
  let activeTab = $state('forms');
  let showCreateModal = $state(false);

  let formConfigRef = $state(null);

  let channelFormData = $state({
    name: '',
    description: '',
    category_id: null,
  });

  let formChannelFormData = $state({
    slug: '',
    workspace_ids: [],
    enabled: false,
    theme: 'light',
    brand_color: '#14b8a6',
    logo_url: '',
    success_message: '',
    redirect_url: '',
  });

  let channelId = $derived(parseInt($currentRoute.path.match(/\/admin\/channels\/(\d+)\/forms/)?.[1]));

  onMount(async () => {
    formBuilderStore.reset();
    await channelCategoriesStore.init();
    await loadChannel();
  });

  onDestroy(() => {
    formBuilderStore.reset();
  });

  async function loadChannel() {
    try {
      loading = true;
      channel = await api.channels.get(channelId);

      channelFormData = channelBasicFormData(channel);

      const config = parseChannelConfig(channel.config);
      formChannelFormData = {
        slug: config.form_slug || '',
        workspace_ids: config.form_workspace_ids || [],
        enabled: channel.status === 'enabled',
        theme: config.form_theme || 'light',
        brand_color: config.form_brand_color || '#14b8a6',
        logo_url: config.form_logo_url || '',
        success_message: config.form_success_message || '',
        redirect_url: config.form_redirect_url || '',
      };
    } catch (err) {
      console.error('Failed to load channel:', err);
      errorToast('Failed to load channel');
    } finally {
      loading = false;
    }
  }

  async function handleSaveSettings() {
    if (!channel) return;

    if (formConfigRef) {
      const validation = formConfigRef.validate();
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
        configRef: formConfigRef,
        enabled: formChannelFormData.enabled,
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

  function handleFormCreated() {
    formBuilderStore.loadForms(channelId);
  }

  const tabs = [
    { id: 'forms', label: () => t('forms.title'), icon: IconForms },
    { id: 'settings', label: () => t('channel.configuration'), icon: IconSettings },
    { id: 'integration', label: () => t('forms.integration.title'), icon: IconCode },
    { id: 'managers', label: () => t('channel.managers'), icon: IconUsers },
  ];
</script>

<ChannelAdminShell
  {loading}
  {channel}
  bind:activeTab
  {tabs}
  subtitle={t('channels.form', 'Form Channel')}
  openUrl={formChannelFormData.slug ? `/forms/${formChannelFormData.slug}` : ''}
  openLabel={t('channel.openForm')}
>
  {#snippet children(tabId)}
    {#if tabId === 'forms'}
      <div class="px-16 py-8">
        <FormBuilder
          {channelId}
          onBack={() => navigate('/admin/channels')}
          onCreateForm={() => showCreateModal = true}
          embedded={false}
        />
      </div>
    {:else if tabId === 'settings'}
      <ChannelAdminSettings bind:channelFormData {saving} onSave={handleSaveSettings}>
        <ChannelFormConfig
          bind:this={formConfigRef}
          bind:formData={formChannelFormData}
        />
      </ChannelAdminSettings>
    {:else if tabId === 'integration'}
      <div class="px-16 py-8 max-w-3xl">
        {#if formChannelFormData.slug}
          <FormIntegrationPanel slug={formChannelFormData.slug} />
        {:else}
          <div class="text-center py-12">
            <IconCode class="w-12 h-12 mx-auto mb-3" style="color: var(--ds-text-subtle);" />
            <p class="text-sm" style="color: var(--ds-text-subtle);">
              {t('channel.formSlugRequired', 'Set a form slug in Settings to enable integration options.')}
            </p>
          </div>
        {/if}
      </div>
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

  {#snippet after()}
    <CreateFormModal
      bind:isOpen={showCreateModal}
      channelId={channelId}
      channelWorkspaceIds={formChannelFormData.workspace_ids}
      onCreated={handleFormCreated}
      onClose={() => showCreateModal = false}
    />
  {/snippet}
</ChannelAdminShell>
