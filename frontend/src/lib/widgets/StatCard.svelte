<script>
  let { icon: Icon, bgColor = null, iconColor, label, value, appearance = 'framed' } = $props();
</script>

<div class="stat-card flex-1" data-appearance={appearance}>
  <div class="flex min-w-0 items-start gap-3">
    <div class="shrink-0 pt-0.5">
      {#if appearance === 'minimal'}
        <Icon class="h-5 w-5" style="color: {iconColor};" aria-hidden="true" />
      {:else}
        <div class="rounded p-2" style="background-color: {bgColor};">
          <Icon class="h-5 w-5" style="color: {iconColor};" aria-hidden="true" />
        </div>
      {/if}
    </div>
    <dl class="min-w-0 flex-1">
      <dt class="truncate text-xs font-medium" style="color: var(--ds-text-subtle);">{label}</dt>
      <dd class="text-xl font-semibold tabular-nums" style="color: var(--ds-text);">{value}</dd>
    </dl>
  </div>
</div>
