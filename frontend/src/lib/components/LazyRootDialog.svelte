<script>
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
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/20"
    role="status"
    data-testid="root-dialog-loading"
  >
    <div class="rounded-lg bg-white px-6 py-5 text-center shadow-lg">
      <div class="animate-spin rounded-full h-8 w-8 border-b-2 border-blue-600 mx-auto mb-3"></div>
      <p class="text-sm text-gray-600">Loading {label}…</p>
    </div>
  </div>
{:then loadedModule}
  {@const Component = loadedModule.default}
  <Component {...componentProps} bind:isOpen />
{:catch}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/20 px-6"
    role="alert"
    data-testid="root-dialog-error"
  >
    <div class="max-w-sm rounded-lg bg-white px-6 py-5 text-center shadow-lg">
      <h1 class="text-lg font-semibold mb-2">Unable to load {label}</h1>
      <p class="text-sm text-gray-600 mb-4">Check your connection, then try again.</p>
      <button
        type="button"
        class="min-h-11 px-5 py-2 rounded-lg bg-blue-600 text-white font-semibold hover:bg-blue-700"
        onclick={() => retryVersion++}
      >Try again</button>
    </div>
  </div>
{/await}
