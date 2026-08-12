<script>
  import { cn } from '../utils/cn.js';

  let {
    value = $bindable(''),
    options = [],
    placeholder = '',
    disabled = false,
    required = false,
    multiple = false,
    size = 'medium',
    id = undefined,
    class: className = '',
    dataTestid = undefined,
    ariaLabel = undefined,
    onchange = undefined,
  } = $props();

  const sizeClasses = $derived(
    {
      small: 'px-3 py-1.5 text-sm',
      medium: 'px-3 py-2 text-sm',
    }[size] || 'px-3 py-2 text-sm',
  );

  const classes = $derived(
    cn(
      'w-full rounded border transition-all duration-200',
      'focus:outline-none focus:ring-2 focus:ring-[var(--ds-border-focused)] focus:ring-opacity-50',
      'disabled:cursor-not-allowed disabled:opacity-50',
      sizeClasses,
      className,
    ),
  );

  function handleChange(event) {
    onchange?.(value, event);
  }
</script>

{#if multiple}
  <select
    {id}
    bind:value
    {disabled}
    {required}
    multiple
    data-testid={dataTestid}
    aria-label={ariaLabel}
    class={classes}
    style="background-color: var(--ds-background-input); border-color: var(--ds-border); color: var(--ds-text);"
    onchange={handleChange}
  >
    {#each options as option}
      <option value={option.value} disabled={option.disabled}>{option.label}</option>
    {/each}
  </select>
{:else}
  <select
    {id}
    bind:value
    {disabled}
    {required}
    data-testid={dataTestid}
    aria-label={ariaLabel}
    class={classes}
    style="background-color: var(--ds-background-input); border-color: var(--ds-border); color: var(--ds-text);"
    onchange={handleChange}
  >
    {#if placeholder}
      <option value="" disabled={required}>{placeholder}</option>
    {/if}
    {#each options as option}
      <option value={option.value} disabled={option.disabled}>{option.label}</option>
    {/each}
  </select>
{/if}
