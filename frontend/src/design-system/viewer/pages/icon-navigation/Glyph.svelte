<script>
  import { HugeiconsIcon } from '@hugeicons/svelte'

  let {
    icon,
    selectedIcon = null,
    mode,
    size = 20,
    selected = false,
  } = $props()

  let renderedIcon = $derived(selected && selectedIcon ? selectedIcon : icon)
</script>

{#if mode === 'iconoir'}
  <span class="raw-icon" style="width: {size}px; height: {size}px;" aria-hidden="true">
    {@html renderedIcon}
  </span>
{:else if mode === 'hugeicons'}
  <HugeiconsIcon icon={renderedIcon} {size} strokeWidth={1.5} />
{:else}
  {@const Icon = renderedIcon}
  {#if mode === 'phosphor'}
    <Icon {size} weight={selected ? 'fill' : 'regular'} />
  {:else if mode === 'lucide-refined'}
    <Icon {size} strokeWidth={selected ? 2 : 1.65} absoluteStrokeWidth />
  {:else}
    <Icon {size} />
  {/if}
{/if}

<style>
  .raw-icon {
    display: inline-grid;
    flex: none;
    place-items: center;
  }

  .raw-icon :global(svg) {
    display: block;
    width: 100%;
    height: 100%;
  }
</style>
