<script>
  let {
    as = 'aside',
    class: className = '',
    header = undefined,
    children,
    footer = undefined,
    scrollClass = '',
    scrollTestid = undefined,
    reserveScrollbarSpace = true,
    scrollContent = true,
    ...restProps
  } = $props();
</script>

<svelte:element this={as} {...restProps} class={['scrollable-sidebar', className]}>
  {#if header}
    <div class="scrollable-sidebar-header">
      {@render header()}
    </div>
  {/if}

  <div
    class={[
      'scrollable-sidebar-content',
      { 'scrollable-sidebar-content-static': !scrollContent },
      { 'scrollable-sidebar-content-stable-gutter': reserveScrollbarSpace },
      scrollClass,
    ]}
    data-testid={scrollTestid}
  >
    {@render children?.()}
  </div>

  {#if footer}
    <div class="scrollable-sidebar-footer">
      {@render footer()}
    </div>
  {/if}
</svelte:element>

<style>
  .scrollable-sidebar {
    display: flex;
    height: 100%;
    min-height: 0;
    max-height: 100%;
    flex-direction: column;
    overflow: hidden;
  }

  .scrollable-sidebar-header,
  .scrollable-sidebar-footer {
    flex: 0 0 auto;
  }

  .scrollable-sidebar-content {
    min-height: 0;
    flex: 1 1 auto;
    overflow-x: hidden;
    overflow-y: auto;
    overscroll-behavior-y: contain;
    scrollbar-color: var(--ds-border) transparent;
    scrollbar-width: thin;
  }

  .scrollable-sidebar-content-stable-gutter {
    scrollbar-gutter: stable;
  }

  .scrollable-sidebar-content-static {
    overflow: hidden;
    scrollbar-gutter: auto;
  }

  .scrollable-sidebar-content::-webkit-scrollbar {
    width: 8px;
  }

  .scrollable-sidebar-content::-webkit-scrollbar-track {
    background: transparent;
  }

  .scrollable-sidebar-content::-webkit-scrollbar-thumb {
    border: 2px solid transparent;
    border-radius: 999px;
    background: var(--ds-border);
    background-clip: content-box;
  }

  .scrollable-sidebar-content::-webkit-scrollbar-thumb:hover {
    background-color: var(--ds-text-subtlest);
  }
</style>
