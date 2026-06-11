<script>
  let {
    title = '',
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
    <h1 class="text-xl font-medium mb-2" style="{textStyle || 'color: var(--ds-text);'}">
      {title}
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