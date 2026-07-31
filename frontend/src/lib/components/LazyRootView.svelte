<script>
  import Button from './Button.svelte';
  import Spinner from './Spinner.svelte';

  let { loader, componentProps = {}, label = 'view' } = $props();
  let retryVersion = $state(0);

  const loadPromise = $derived.by(() => {
    retryVersion;
    return loader();
  });
</script>

{#await loadPromise}
  <div
    class="flex flex-1 min-h-[40vh] items-center justify-center"
    style="color: var(--ds-text);"
    role="status"
    data-testid="root-view-loading"
  >
    <div class="text-center">
      <Spinner class="mx-auto mb-3" />
      <p class="text-sm" style="color: var(--ds-text-subtle);">Loading {label}…</p>
    </div>
  </div>
{:then loadedModule}
  {@const Component = loadedModule.default}
  <Component {...componentProps} />
{:catch}
  <div
    class="flex flex-1 min-h-[40vh] items-center justify-center px-6"
    style="color: var(--ds-text);"
    role="alert"
    data-testid="root-view-error"
  >
    <div class="text-center max-w-sm">
      <h1 class="text-lg font-semibold mb-2">Unable to load {label}</h1>
      <p class="mb-4 text-sm" style="color: var(--ds-text-subtle);">
        Check your connection, then try again.
      </p>
      <!-- shortcut-guard-exempt: retrying a failed lazy import is a recovery action, not a form submission. -->
      <Button
        variant="primary"
        size="large"
        dataTestid="root-view-retry"
        onclick={() => retryVersion++}
      >Try again</Button>
    </div>
  </div>
{/await}
