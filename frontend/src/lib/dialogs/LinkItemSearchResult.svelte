<script>
  import { FileText } from '@lucide/svelte';
  import ItemKey from '../features/items/ItemKey.svelte';
  import { itemTypeIconMap } from '../utils/icons.js';
  import { t } from '../stores/i18n.svelte.js';

  let {
    result,
    highlighted = false,
    onhighlight = null,
    onselect = null,
  } = $props();

  const iconMap = itemTypeIconMap;
  let itemTypeIcon = $derived(result.item_type_icon);
  let itemTypeColor = $derived(result.item_type_color || 'var(--ds-icon)');
</script>

<button
  type="button"
  data-testid="link-search-result"
  data-result-type={result.type || 'item'}
  data-result-id={result.id}
  class="w-full text-left px-3 py-2 cursor-pointer border-b last:border-b-0 transition-colors"
  style="color: var(--ds-text); border-color: var(--ds-border); {highlighted ? 'background-color: var(--ds-surface-raised-hovered);' : ''}"
  onmouseenter={onhighlight}
  onclick={onselect}
>
  <div class="flex items-center gap-2">
    {#if itemTypeIcon && iconMap[itemTypeIcon]}
      {@const ItemTypeIconComponent = iconMap[itemTypeIcon]}
      <div
        class="w-7 h-7 rounded flex items-center justify-center flex-shrink-0"
        style="background-color: color-mix(in srgb, {itemTypeColor} 16%, transparent); color: {itemTypeColor};"
      >
        <ItemTypeIconComponent class="w-4 h-4" />
      </div>
    {:else}
      <div
        class="w-7 h-7 rounded flex items-center justify-center flex-shrink-0"
        style="background-color: var(--ds-background-neutral); color: var(--ds-icon-subtle);"
      >
        <FileText class="w-4 h-4" />
      </div>
    {/if}

    <div class="flex-1 min-w-0">
      <div class="font-medium text-sm truncate">{result.title}</div>
      <div class="text-xs" style="color: var(--ds-text-subtle);">
        {#if result.type === 'test_case'}
          {result.description || `Test Case #${result.id}`}
        {:else}
          <ItemKey item={result} />
        {/if}
      </div>
    </div>

    {#if result.type === 'test_case'}
      <span
        class="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded-full flex-shrink-0"
        style="background-color: var(--ds-accent-purple-subtle); color: var(--ds-icon-accent-purple);"
      >
        {t('items.testCase')}
      </span>
    {/if}
  </div>
</button>
