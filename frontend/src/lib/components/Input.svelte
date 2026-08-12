<script>
  import { cn } from '../utils/cn.js';

  /**
   * @type {{
   *   type?: string,
   *   value?: any,
   *   placeholder?: string,
   *   disabled?: boolean,
   *   required?: boolean,
   *   autofocus?: boolean,
   *   size?: string,
   *   min?: any,
   *   max?: any,
   *   step?: any,
   *   id?: string,
   *   name?: string,
   *   pattern?: string,
   *   title?: string,
   *   autocomplete?: string,
   *   readonly?: boolean,
   *   minlength?: number,
   *   maxlength?: number,
   *   class?: string,
   *   dataTestid?: string,
   *   ariaLabel?: string,
   *   ariaDescribedby?: string,
   *   inputRef?: any,
   *   oninput?: (e?: any) => void,
   *   onchange?: (e?: any) => void,
   *   onfocus?: (e?: any) => void,
   *   onblur?: (e?: any) => void,
   *   onkeydown?: (e?: any) => void,
   *   onkeyup?: (e?: any) => void,
   * }}
   */
  let {
    type = 'text',
    value = $bindable(''),
    placeholder = '',
    disabled = false,
    required = false,
    autofocus = false,
    size = 'medium',
    min = undefined,
    max = undefined,
    step = undefined,
    id = undefined,
    name = undefined,
    pattern = undefined,
    title = undefined,
    autocomplete = undefined,
    readonly = false,
    minlength = undefined,
    maxlength = undefined,
    class: className = '',
    dataTestid = undefined,
    ariaLabel = undefined,
    ariaDescribedby = undefined,
    // Optional ref binding for parent components that need the raw input element
    inputRef = $bindable(null),
    // Event handlers
    oninput = undefined,
    onchange = undefined,
    onfocus = undefined,
    onblur = undefined,
    onkeydown = undefined,
    onkeyup = undefined
  } = $props();
  export { className as class };

  // Size variants
  const sizeClasses = $derived({
    small: 'px-3 py-1.5 text-sm',
    medium: 'px-4 py-3'
  }[size] || 'px-4 py-3');

  // Combine all classes
  const allClasses = $derived(cn(
    'w-full rounded border transition-all duration-200',
    'focus:outline-none focus:ring-2 focus:ring-[var(--ds-border-focused)] focus:ring-opacity-50',
    sizeClasses,
    className
  ));
  /** @type {any} */
  const autocompleteValue = $derived(autocomplete);
</script>

<!-- svelte-ignore a11y_autofocus -->
<input
  {type}
  {id}
  {name}
  {pattern}
  {title}
  autocomplete={autocompleteValue}
  {readonly}
  {minlength}
  {maxlength}
  bind:value
  bind:this={inputRef}
  data-testid={dataTestid}
  aria-label={ariaLabel}
  aria-describedby={ariaDescribedby}
  {placeholder}
  {disabled}
  {required}
  {autofocus}
  {min}
  {max}
  {step}
  class={allClasses}
  style="background-color: var(--ds-background-input); border-color: var(--ds-border); color: var(--ds-text);"
  {oninput}
  {onchange}
  {onfocus}
  {onblur}
  {onkeydown}
  {onkeyup}
/>
