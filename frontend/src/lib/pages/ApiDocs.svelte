<script>
  import { onMount, onDestroy } from 'svelte';

  // Scalar pulls in Vue + the full reference UI (~3MB pre-gzip). Load it
  // dynamically so this weight only ships when /api-docs is visited and
  // stays out of the main bundle for everyone else.
  let mountEl;
  let app;
  let loading = $state(true);
  let loadError = $state(null);

  onMount(async () => {
    try {
      const [{ createApiReference }] = await Promise.all([
        import('@scalar/api-reference'),
        import('@scalar/api-reference/style.css'),
      ]);
      if (!mountEl) return;
      app = createApiReference(mountEl, {
        url: '/rest/api/v1/openapi.json',
        hideDarkModeToggle: false,
        defaultHttpClient: { targetKey: 'shell', clientKey: 'curl' },
      });
    } catch (err) {
      console.error('Failed to load Scalar API reference', err);
      loadError = err?.message || 'Failed to load API reference';
    } finally {
      loading = false;
    }
  });

  onDestroy(() => {
    app?.unmount?.();
  });
</script>

<div class="api-docs-host" bind:this={mountEl}>
  {#if loading}
    <p class="status">Loading API reference…</p>
  {:else if loadError}
    <p class="status status--error">{loadError}</p>
  {/if}
</div>

<style>
  .api-docs-host {
    height: 100%;
    min-height: calc(100vh - var(--ds-app-header-height, 0px));
    width: 100%;
    overflow: hidden;
  }
  .api-docs-host :global(.scalar-app),
  .api-docs-host :global(.scalar-api-reference) {
    height: 100%;
  }
  .status {
    padding: 2rem;
    color: var(--ds-text-subtle);
    text-align: center;
  }
  .status--error {
    color: var(--ds-text-danger, #ef4444);
  }
</style>
