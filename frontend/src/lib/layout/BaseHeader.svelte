<script>
  let {
    title = '',
    badge = '',
    badgeStyle = 'background-color: var(--ctx-active-bg, var(--ds-accent-blue-subtler)); color: var(--ctx-active-text, var(--ds-accent-blue)); backdrop-filter: var(--ctx-backdrop, none);',
    subtitle = '',
    description = '',
    icon = null,
    count = null,
    children = null,
    actions = null,
    textStyle = '',
    subtitleStyle = '',
    iconStyle = ''
  } = $props();

  const subtitleText = $derived(subtitle || description);
  const iconStyleProp = $derived(iconStyle || 'color: var(--ds-icon-subtle);');
  const subtitleStyleProp = $derived(subtitleStyle || 'color: var(--ds-text-subtle);');
</script>

<div class="flex items-center justify-between mb-{count ? '6' : '8'}">
  <div>
    <h1 class="flex items-baseline gap-2 text-xl font-medium mb-2" style="{textStyle || 'color: var(--ds-text);'}">
      {title}
      {#if badge}
        <span class="text-xs font-medium px-1.5 py-0.5 rounded" style={badgeStyle}>{badge}</span>
      {/if}
    </h1>
    {#if subtitleText || count !== null}
      <div class="flex items-center gap-2 text-sm" style="{subtitleStyleProp}">
        {#if icon && subtitleText}
          {@const Icon = icon}
          <Icon class="w-3.5 h-3.5" style={iconStyleProp} />
        {/if}
        {#if subtitleText}
          <span>{subtitleText}</span>
        {/if}
        {#if count !== null}
          {#if subtitleText}<span style="color: var(--ds-text-disabled);">•</span>{/if}
          <span style="color: var(--ds-text-disabled);">{count}</span>
        {/if}
      </div>
    {/if}
  </div>

  {#if actions}
    <div>
      {@render actions()}
    </div>
  {/if}
</div>