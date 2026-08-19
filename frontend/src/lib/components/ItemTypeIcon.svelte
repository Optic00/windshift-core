<script>
  import { FileText } from '@lucide/svelte';
  import { itemTypeIconMap } from '../utils/icons.js';

  let {
    itemType = null,
    icon = null,
    color = null,
    size = 16,
    title = undefined,
    ariaLabel = undefined,
    class: className = ''
  } = $props();

  const resolvedIconName = $derived(icon || itemType?.icon);
  const ResolvedIcon = $derived(itemTypeIconMap[resolvedIconName] || FileText);
  const resolvedColor = $derived(color || itemType?.color || '#3b82f6');
  const resolvedTitle = $derived(title ?? itemType?.name);
</script>

<span
  class="item-type-icon {className}"
  style="--item-type-color: {resolvedColor};"
  title={resolvedTitle}
  aria-label={ariaLabel}
  aria-hidden={ariaLabel ? undefined : 'true'}
>
  <ResolvedIcon {size} strokeWidth={1.9} />
</span>

<style>
  .item-type-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    overflow: hidden;
    color: #fff;
    background-color: var(--item-type-color);
    background-image: linear-gradient(
      145deg,
      color-mix(in srgb, var(--item-type-color) 76%, #fff 24%),
      color-mix(in srgb, var(--item-type-color) 84%, #334155 16%)
    );
    border: 1px solid color-mix(in srgb, var(--item-type-color) 68%, #fff 32%);
    box-shadow:
      inset 0 1px 0 rgb(255 255 255 / 22%),
      0 1px 2px rgb(15 23 42 / 14%);
  }

  .item-type-icon :global(svg) {
    filter: drop-shadow(0 1px 1px rgb(15 23 42 / 18%));
  }
</style>
