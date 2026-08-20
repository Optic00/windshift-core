<script>
  import { itemTypeIconMap } from '../utils/icons.js';

  // Named sizes keep tile and icon in sync across every usage:
  // xs 16/12 (dense rows, board cards), sm 20/14 (list rows), md 24/16 (admin lists),
  // lg 28/18 (search results, pickers).
  const SIZES = {
    xs: { tile: 16, icon: 12 },
    sm: { tile: 20, icon: 14 },
    md: { tile: 24, icon: 16 },
    lg: { tile: 28, icon: 18 },
  };

  let {
    itemType = null,
    icon = null,
    color = null,
    size = 'md',
    variant = 'tile',
    title = undefined,
    ariaLabel = undefined,
    testId = undefined,
    class: className = ''
  } = $props();

  const resolvedIconName = $derived(String(icon || itemType?.icon || ''));
  const ResolvedIcon = $derived(itemTypeIconMap[resolvedIconName] || itemTypeIconMap.FileText);
  const resolvedColor = $derived(color || itemType?.color || (variant === 'tile' ? '#3b82f6' : '#6b7280'));
  const resolvedTitle = $derived(title ?? itemType?.name);

  const tokens = $derived(SIZES[size] || SIZES.md);
  const iconSize = $derived(typeof size === 'number' ? size : tokens.icon);
  const radius = $derived(variant === 'tinted' && tokens.tile >= 24 ? '50%' : (tokens.tile <= 20 ? '3px' : '5px'));
</script>

{#if variant === 'plain'}
  <span
    class="item-type-icon-plain {className}"
    style="color: {resolvedColor};"
    title={resolvedTitle}
    aria-label={ariaLabel}
    aria-hidden={ariaLabel ? undefined : 'true'}
    data-testid={testId}
  >
    <ResolvedIcon size={iconSize} strokeWidth={1.9} />
  </span>
{:else if variant === 'tinted'}
  <span
    class="item-type-icon {className}"
    style="width: {tokens.tile}px; height: {tokens.tile}px; border-radius: {radius}; color: {resolvedColor}; background-color: color-mix(in srgb, {resolvedColor} 14%, transparent);"
    title={resolvedTitle}
    aria-label={ariaLabel}
    aria-hidden={ariaLabel ? undefined : 'true'}
    data-testid={testId}
  >
    <ResolvedIcon size={iconSize} strokeWidth={1.9} />
  </span>
{:else}
  <span
    class="item-type-icon {className}"
    style="width: {tokens.tile}px; height: {tokens.tile}px; border-radius: {radius}; background-color: {resolvedColor};"
    title={resolvedTitle}
    aria-label={ariaLabel}
    aria-hidden={ariaLabel ? undefined : 'true'}
    data-testid={testId}
  >
    <ResolvedIcon size={iconSize} strokeWidth={1.9} />
  </span>
{/if}

<style>
  .item-type-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    overflow: hidden;
    flex-shrink: 0;
    color: #fff;
  }

  .item-type-icon-plain {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }
</style>
