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
</script>

<BaseHeader
  title={viewName}
  badge={collection}
  {subtitle}
  {actions}
  textStyle={textStyle || 'color: var(--ctx-text, var(--ds-text));'}
  subtitleStyle={subtleTextStyle || 'color: var(--ctx-text-subtle, var(--ds-text-subtle));'}
  icon={null}
  count={null}
  children={null}
  marginClass="mb-4"
/>