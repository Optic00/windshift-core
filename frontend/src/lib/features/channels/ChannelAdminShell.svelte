<script>
  import { IconArrowLeft, IconExternalLink } from '@tabler/icons-svelte-runes';
  import { t } from '../../stores/i18n.svelte.js';
  import Button from '../../components/Button.svelte';
  import Spinner from '../../components/Spinner.svelte';

  let {
    loading = false,
    channel = null,
    activeTab = $bindable('settings'),
    tabs = [],
    subtitle = '',
    openUrl = '',
    openLabel = '',
    children,
    after = undefined,
  } = $props();
</script>

{#if loading}
  <div class="flex-1 flex items-center justify-center py-24">
    <Spinner />
  </div>
{:else if channel}
  <div class="flex-1 flex flex-col overflow-hidden">
    <div class="px-16 pt-8 pb-0">
      <div class="mb-4">
        <a
          href="/admin/channels"
          class="inline-flex items-center gap-1 text-sm transition-colors"
          style="color: var(--ds-text-subtle);"
          onmouseenter={(e) => e.currentTarget.style.color = 'var(--ds-text)'}
          onmouseleave={(e) => e.currentTarget.style.color = 'var(--ds-text-subtle)'}
        >
          <IconArrowLeft class="w-4 h-4" />
          {t('channels.title')}
        </a>
      </div>

      <div class="flex items-center justify-between mb-6">
        <div>
          <h1 class="text-2xl font-semibold" style="color: var(--ds-text);">
            {channel.name}
          </h1>
          <p class="text-sm mt-1" style="color: var(--ds-text-subtle);">
            {subtitle}
          </p>
        </div>
        {#if openUrl}
          <Button
            onclick={() => window.open(openUrl, '_blank')}
            variant="default"
            size="small"
            icon={IconExternalLink}
          >
            {openLabel}
          </Button>
        {/if}
      </div>

      <nav class="flex gap-6 border-b" style="border-color: var(--ds-border);">
        {#each tabs as tab}
          <button
            data-testid="channel-tab-{tab.id}"
            onclick={() => activeTab = tab.id}
            class="relative py-3 text-sm font-medium transition-colors {
              activeTab === tab.id
                ? 'text-[var(--ds-interactive)]'
                : 'text-[var(--ds-text-subtle)] hover:text-[var(--ds-text)]'
            }"
          >
            <div class="flex items-center gap-2">
              <tab.icon class="w-4 h-4" />
              <span>{tab.label()}</span>
            </div>
            {#if activeTab === tab.id}
              <div class="absolute bottom-0 left-0 right-0 h-0.5 bg-[var(--ds-interactive)]"></div>
            {/if}
          </button>
        {/each}
      </nav>
    </div>

    <div class="flex-1 overflow-y-auto">
      {@render children?.(activeTab)}
    </div>
  </div>

  {@render after?.()}
{/if}
