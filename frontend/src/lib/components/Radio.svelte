<script>
  let {
    id = undefined,
    name = undefined,
    value = undefined,
    groupValue = $bindable(null),
    checked = false,
    disabled = false,
    class: className = '',
    dataTestid = undefined,
    onchange = undefined,
  } = $props();
  export { className as class };

  const isGrouped = $derived(groupValue !== null);
  const isChecked = $derived(isGrouped ? groupValue === value : checked);

  function handleChange(event) {
    if (!event.currentTarget.checked) return;
    if (isGrouped) groupValue = value;
    onchange?.(value, event);
  }
</script>

<input
  {id}
  type="radio"
  {name}
  {value}
  {disabled}
  checked={isChecked}
  onchange={handleChange}
  data-testid={dataTestid}
  class="h-4 w-4 accent-[var(--ds-interactive)] {className}"
/>
