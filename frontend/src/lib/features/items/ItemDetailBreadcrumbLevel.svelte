<script>
  import ItemTypeIcon from '../../components/ItemTypeIcon.svelte';
  import Tooltip from '../../components/Tooltip.svelte';

  let {
    itemType = null,
    iconTooltip = itemType?.name,
    iconTitle = undefined,
    iconSlotTestId = undefined,
    iconTriggerTestId = undefined,
    iconTestId = undefined,
    oniconclick = null,
    iconOverlay = null,
    class: className = '',
    style: customStyle = '',
    children: levelContent
  } = $props();
</script>

<div class="flex min-w-0 items-center gap-2 {className}" style={customStyle}>
  {#if itemType}
    <div
      data-testid={iconSlotTestId}
      class="relative inline-flex h-6 w-6 flex-shrink-0 items-center justify-center leading-none"
    >
      <Tooltip
        content={iconTooltip}
        class="inline-flex h-full w-full items-center justify-center leading-none"
      >
        {#snippet children()}
          {#if oniconclick}
            <button
              type="button"
              data-testid={iconTriggerTestId}
              data-item-type-id={itemType.id}
              onclick={oniconclick}
              class="inline-flex h-full w-full cursor-pointer items-center justify-center rounded leading-none focus:outline-none focus:ring-2 focus:ring-blue-500"
              title={iconTitle}
            >
              <ItemTypeIcon {itemType} testId={iconTestId} />
            </button>
          {:else}
            <span class="inline-flex h-full w-full items-center justify-center leading-none cursor-help">
              <ItemTypeIcon {itemType} testId={iconTestId} />
            </span>
          {/if}
        {/snippet}
      </Tooltip>

      {#if iconOverlay}
        {@render iconOverlay()}
      {/if}
    </div>
  {/if}

  {@render levelContent()}
</div>
