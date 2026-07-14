<script>
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
    role="status"
    data-testid="root-view-loading"
  >
    <div class="text-center">
      <div class="animate-spin rounded-full h-8 w-8 border-b-2 border-blue-600 mx-auto mb-3"></div>
      <p class="text-sm text-gray-600">Loading {label}…</p>
    </div>
  </div>
{:then loadedModule}
  {@const Component = loadedModule.default}
  <Component {...componentProps} />
{:catch}
  <div
    class="flex flex-1 min-h-[40vh] items-center justify-center px-6"
    role="alert"
    data-testid="root-view-error"
  >
    <div class="text-center max-w-sm">
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
