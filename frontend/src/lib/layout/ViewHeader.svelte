<script>
  import { t } from '../stores/i18n.svelte.js';
  import BaseHeader from './BaseHeader.svelte';

  let {
    workspaceName = '',
    collection = '',
    viewName = '',
    itemCount = null,
    actionButtons = null,
    hasGradient = false,
    textStyle = '',
    subtleTextStyle = '',
    actions = null,
  } = $props();

  // Build subtitle from workspace and item count
  let subtitle = $derived(
    workspaceName + (itemCount !== null ? ` • ${itemCount} ${t('layout.items')}` : '')
  );

  // Build title with collection badge
  let title = $derived(
    viewName + (collection ? 
      `<span class="text-xs font-medium px-1.5 py-0.5 rounded" style="background-color: var(--ctx-active-bg, var(--ds-accent-blue-subtler)); color: var(--ctx-active-text, var(--ds-accent-blue)); backdrop-filter: var(--ctx-backdrop, none);">${collection}</span>` 
      : '')
  );
</script>

<BaseHeader
  {title}
  {subtitle}
  {actions}
  {textStyle}
  subtitleStyle={subtleTextStyle || 'color: var(--ctx-text-subtle, var(--ds-text-subtle));'}
  icon={null}
  count={null}
  children={null}
/>