<script>
  let {
    id = undefined,
    name = undefined,
    accept = undefined,
    multiple = false,
    disabled = false,
    tabindex = undefined,
    ariaHidden = undefined,
    class: className = '',
    style = '',
    dataTestid = undefined,
    inputRef = $bindable(null),
    onchange = undefined,
  } = $props();
  export { className as class };
</script>

<input
  {id}
  {name}
  type="file"
  {accept}
  {multiple}
  {disabled}
  {tabindex}
  aria-hidden={ariaHidden}
  bind:this={inputRef}
  data-testid={dataTestid}
  class={className}
  {style}
  {onchange}
/>
