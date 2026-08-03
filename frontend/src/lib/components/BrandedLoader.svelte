<script>
  import { APP_NAME } from '../constants.js';

  let { label = `Loading ${APP_NAME}…`, detail = '', fullViewport = true } = $props();
</script>

<div
  class="flex w-full items-center justify-center px-6 {fullViewport ? 'min-h-dvh' : 'h-full'}"
  style="color: var(--ds-text);"
  role="status"
  data-testid="branded-loader"
>
  <div class="text-center">
    <img
      src="windshift-3.svg"
      alt={APP_NAME}
      class="mx-auto mb-4 h-16 w-16 motion-safe:animate-pulse"
      data-testid="branded-loader-logo"
    />
    <p class="text-sm" style="color: var(--ds-text-subtle);">{label}</p>
    {#if detail}
      <p class="mt-1 text-sm" style="color: var(--ds-text-subtlest);">{detail}</p>
    {/if}
  </div>
</div>
