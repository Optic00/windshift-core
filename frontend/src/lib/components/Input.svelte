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
   *   enterkeyhint?: 'enter' | 'done' | 'go' | 'next' | 'previous' | 'search' | 'send',
   *   readonly?: boolean,
   *   minlength?: number,
   *   maxlength?: number,
   *   list?: string,
   *   class?: string,
   *   style?: string,
   *   dataTestid?: string,
   *   dataAutofocus?: boolean,
   *   ariaLabel?: string,
   *   ariaDescribedby?: string,
   *   inputRef?: any,
   *   oninput?: (e?: any) => void,
   *   onchange?: (e?: any) => void,
   *   onfocus?: (e?: any) => void,
   *   onblur?: (e?: any) => void,
   *   onclick?: (e?: any) => void,
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
    enterkeyhint = undefined,
    readonly = false,
    minlength = undefined,
    maxlength = undefined,
    list = undefined,
    class: className = '',
    style = '',
    dataTestid = undefined,
    dataAutofocus = false,
    ariaLabel = undefined,
    ariaDescribedby = undefined,
    // Optional ref binding for parent components that need the raw input element
    inputRef = $bindable(null),
    // Event handlers
    oninput = undefined,
    onchange = undefined,
    onfocus = undefined,
    onblur = undefined,
    onclick = undefined,
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
  {enterkeyhint}
  {readonly}
  {minlength}
  {maxlength}
  {list}
  bind:value
  bind:this={inputRef}
  data-testid={dataTestid}
  data-autofocus={dataAutofocus || undefined}
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
  style="background-color: var(--ds-background-input); border-color: var(--ds-border); color: var(--ds-text); {style}"
  {oninput}
  {onchange}
  {onfocus}
  {onblur}
  {onclick}
  {onkeydown}
  {onkeyup}
/>
