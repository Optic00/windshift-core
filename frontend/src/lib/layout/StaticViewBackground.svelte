<script>
  let {
    backgroundStyle = '',
    contextVars = '',
    class: className = '',
    contentClass = 'p-6',
    contentStyle = '',
    rootStyle = '',
    testid = undefined,
    children
  } = $props();
</script>

<div
  class={`min-h-screen w-fit min-w-full ${className}`}
  style={`${backgroundStyle} ${contextVars} background-attachment: fixed; ${rootStyle}`}
  data-testid={testid}
>
  <div class={contentClass} style={contentStyle}>
    {#if children}
      {@render children()}
    {/if}
  </div>
</div>
