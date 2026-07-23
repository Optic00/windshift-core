<script>
  import LinkComponent from '../../components/Link.svelte';

  let {
    item,
    workspace = null,
    size = 'default',
    style = "color: var(--ds-text-subtle);",
    href = null,
    onClick = null,
    monospace = true,
    class: className = '',
  } = $props();

  let displayKey = $derived((() => {
    const key = item.workspace_key || workspace?.key;
    return key ? `${key}-${item.workspace_item_number}` : `ITEM-${item.workspace_item_number}`;
  })());

  let interactive = $derived(!!(href || onClick));
  let sizeClass = $derived(
    size === 'compact' ? 'text-[10px] leading-4 tracking-[0.02em]' : 'text-xs',
  );
  let classes = $derived(
    `${sizeClass}${monospace ? ' font-mono' : ''} flex-shrink-0 whitespace-nowrap${interactive ? ' hover:underline cursor-pointer' : ''} ${className}`,
  );
</script>

{#if href}
  <LinkComponent {href} {onClick} class={classes} {style}>
    {displayKey}
  </LinkComponent>
{:else if onClick}
  <button class={classes} {style} onclick={onClick} type="button">
    {displayKey}
  </button>
{:else}
  <span class={classes} {style}>
    {displayKey}
  </span>
{/if}
