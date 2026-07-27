<script>

  /** Semantic link preserving browser navigation and optional custom clicks. */

  let {
    href = '',
    active = false,
    disabled = false,
    onClick = null,
    style = '',
    onmouseenter = null,
    onmouseleave = null,
    target = undefined,
    rel = undefined,
    class: className = '',
    element: anchorElement = $bindable(undefined),
    children,
    ...rest
  } = $props();

  // Preserve native modified, non-primary, targeted, and prevented clicks.
  function handleClick(event) {
    if (disabled) {
      event.preventDefault();
      return;
    }
    if (event.defaultPrevented) return;
    if (target && target !== '_self') return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    if (event.button !== undefined && event.button !== 0) return;
    onClick?.(event);
  }
</script>

<a
  bind:this={anchorElement}
  {href}
  {target}
  {rel}
  onclick={handleClick}
  {onmouseenter}
  {onmouseleave}
  class={className}
  {style}
  aria-current={active ? 'page' : undefined}
  aria-disabled={disabled}
  tabindex={disabled ? -1 : 0}
  {...rest}
>
  {@render children?.()}
</a>
