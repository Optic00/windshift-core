<script>
  import Button from './Button.svelte';
  import ModalBackdrop from './ModalBackdrop.svelte';
  import Spinner from './Spinner.svelte';

  let {
    loader,
    componentProps = {},
    label = 'dialog',
    isOpen = $bindable(false),
  } = $props();
  let retryVersion = $state(0);

  const loadPromise = $derived.by(() => {
    retryVersion;
    return loader();
  });
</script>

{#await loadPromise}
  <ModalBackdrop
    show={true}
    opacity={0.4}
    closeOnClick={false}
    closeOnEscape={false}
    transition={false}
    ariaLabelledBy="root-dialog-loading-label"
  >
    <div
      class="rounded-lg px-6 py-5 text-center"
      style="background-color: var(--ds-surface-raised); color: var(--ds-text); box-shadow: var(--ds-shadow-raised);"
      role="status"
      data-testid="root-dialog-loading"
    >
      <Spinner class="mx-auto mb-3" />
      <p id="root-dialog-loading-label" class="text-sm" style="color: var(--ds-text-subtle);">
        Loading {label}…
      </p>
    </div>
  </ModalBackdrop>
{:then loadedModule}
  {@const Component = loadedModule.default}
  <Component {...componentProps} bind:isOpen />
{:catch}
  <ModalBackdrop
    show={true}
    opacity={0.4}
    closeOnClick={false}
    closeOnEscape={false}
    transition={false}
    ariaLabelledBy="root-dialog-error-title"
  >
    <div
      class="mx-2 max-w-sm rounded-lg px-6 py-5 text-center"
      style="background-color: var(--ds-surface-raised); color: var(--ds-text); box-shadow: var(--ds-shadow-raised);"
      role="alert"
      data-testid="root-dialog-error"
    >
      <h1 id="root-dialog-error-title" class="mb-2 text-lg font-semibold">
        Unable to load {label}
      </h1>
      <p class="mb-4 text-sm" style="color: var(--ds-text-subtle);">
        Check your connection, then try again.
      </p>
      <!-- shortcut-guard-exempt: retrying a failed lazy import is a recovery action, not a form submission. -->
      <Button
        variant="primary"
        size="large"
        dataTestid="root-dialog-retry"
        onclick={() => retryVersion++}
      >Try again</Button>
    </div>
  </ModalBackdrop>
{/await}
