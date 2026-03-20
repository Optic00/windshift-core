<script>
  import { onMount } from 'svelte';
  import { Loader2 } from 'lucide-svelte';
  import { api } from '../api.js';
  import { t } from '../stores/i18n.svelte.js';
  import { errorToast, successToast } from '../stores/toasts.svelte.js';
  import Spinner from '../components/Spinner.svelte';

  const FEATURE_KEYS = [
    'ai_chat',
    'daily_briefing',
    'plan_my_day',
    'catch_me_up',
    'find_similar',
    'decompose',
    'release_notes',
    'dependency_analysis',
  ];

  let loading = $state(true);
  let saving = $state(false);
  let config = $state({});
  let connections = $state([]);

  onMount(async () => {
    await loadConfig();
  });

  async function loadConfig() {
    loading = true;
    try {
      const data = await api.aiFeatures.getConfig();
      config = data.config ?? {};
      connections = data.connections ?? [];
    } catch (err) {
      errorToast(t('settings.aiFeatures.loadFailed'));
      console.error('Failed to load AI features config:', err);
    } finally {
      loading = false;
    }
  }

  function getMode(key) {
    return config[key]?.mode || 'default';
  }

  function getConnectionId(key) {
    return config[key]?.connection_id || 0;
  }

  function getSchedule(key) {
    return config[key]?.schedule || 'daily';
  }

  async function setMode(key, mode) {
    config = {
      ...config,
      [key]: {
        mode,
        connection_id: mode === 'specific' ? (config[key]?.connection_id || (connections[0]?.id ?? 0)) : 0,
        ...(key === 'daily_briefing' ? { schedule: config[key]?.schedule || 'daily' } : {}),
      },
    };
    await save();
  }

  async function setConnectionId(key, connectionId) {
    config = {
      ...config,
      [key]: {
        ...config[key],
        connection_id: parseInt(connectionId, 10),
      },
    };
    await save();
  }

  async function setSchedule(key, schedule) {
    config = {
      ...config,
      [key]: {
        ...config[key],
        schedule,
      },
    };
    await save();
  }

  async function save() {
    saving = true;
    try {
      const result = await api.aiFeatures.updateConfig(config);
      config = result.config ?? config;
      successToast(t('settings.aiFeatures.saveSuccess'));
    } catch (err) {
      errorToast(t('settings.aiFeatures.saveFailed'));
      console.error('Failed to save AI features config:', err);
    } finally {
      saving = false;
    }
  }
</script>

{#if loading}
  <div class="flex items-center justify-center py-12">
    <Spinner />
  </div>
{:else}
  {#if connections.length === 0}
    <div class="mb-6 text-sm rounded p-4 border" style="background-color: var(--ds-surface-raised); border-color: var(--ds-border); color: var(--ds-text-subtle);">
      {t('settings.aiFeatures.noConnections')}
    </div>
  {/if}

  <div class="space-y-4">
    {#each FEATURE_KEYS as key}
      {@const mode = getMode(key)}
      <div class="border rounded p-5" style="background-color: var(--ds-surface-raised); border-color: var(--ds-border);">
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <h3 class="text-sm font-medium" style="color: var(--ds-text);">
              {t(`settings.aiFeatures.features.${key}.name`)}
            </h3>
            <p class="text-xs mt-0.5" style="color: var(--ds-text-subtle);">
              {t(`settings.aiFeatures.features.${key}.description`)}
            </p>
          </div>

          <div class="flex items-center gap-2 shrink-0">
            {#if saving}
              <Loader2 class="w-4 h-4 animate-spin" style="color: var(--ds-text-subtle);" />
            {/if}
            <select
              class="text-sm rounded border px-2 py-1.5"
              style="background-color: var(--ds-surface); border-color: var(--ds-border); color: var(--ds-text);"
              value={mode}
              onchange={(e) => setMode(key, e.target.value)}
            >
              <option value="default">{t('settings.aiFeatures.modeDefault')}</option>
              <option value="specific">{t('settings.aiFeatures.modeSpecific')}</option>
              <option value="disabled">{t('settings.aiFeatures.modeDisabled')}</option>
            </select>
          </div>
        </div>

        {#if mode === 'specific'}
          <div class="mt-3 pt-3 border-t" style="border-color: var(--ds-border);">
            <select
              class="text-sm rounded border px-2 py-1.5 w-full max-w-xs"
              style="background-color: var(--ds-surface); border-color: var(--ds-border); color: var(--ds-text);"
              value={getConnectionId(key)}
              onchange={(e) => setConnectionId(key, e.target.value)}
            >
              <option value="0" disabled>{t('settings.aiFeatures.selectConnection')}</option>
              {#each connections as conn}
                <option value={conn.id}>{conn.name}</option>
              {/each}
            </select>
          </div>
        {/if}

        {#if key === 'daily_briefing' && mode !== 'disabled'}
          <div class="mt-3 pt-3 border-t" style="border-color: var(--ds-border);">
            <label class="block text-xs mb-1" style="color: var(--ds-text-subtle);">
              {t('settings.aiFeatures.scheduleLabel')}
            </label>
            <select
              class="text-sm rounded border px-2 py-1.5 w-full max-w-xs"
              style="background-color: var(--ds-surface); border-color: var(--ds-border); color: var(--ds-text);"
              value={getSchedule(key)}
              onchange={(e) => setSchedule(key, e.target.value)}
            >
              <option value="daily">{t('settings.aiFeatures.scheduleDaily')}</option>
              <option value="every_6h">{t('settings.aiFeatures.scheduleEvery6h')}</option>
            </select>
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}
