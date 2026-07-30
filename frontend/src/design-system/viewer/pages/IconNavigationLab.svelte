<script>
  import NavigationMock from './icon-navigation/NavigationMock.svelte'

  const candidates = [
    { id: 'lucide', label: 'Lucide baseline', load: () => import('./icon-navigation/candidates/lucide.js') },
    { id: 'lucide-refined', label: 'Lucide refined', load: () => import('./icon-navigation/candidates/lucide-refined.js') },
    { id: 'iconoir', label: 'Iconoir', load: () => import('./icon-navigation/candidates/iconoir.js') },
    { id: 'carbon', label: 'Carbon', load: () => import('./icon-navigation/candidates/carbon.js') },
    { id: 'phosphor', label: 'Phosphor', load: () => import('./icon-navigation/candidates/phosphor.js') },
    { id: 'hugeicons', label: 'Hugeicons Free', load: () => import('./icon-navigation/candidates/hugeicons.js') },
  ]

  let selectedId = $state('lucide-refined')
  let candidate = $state(null)
  let loadError = $state(false)

  $effect(() => {
    const selected = candidates.find((item) => item.id === selectedId)
    let active = true
    candidate = null
    loadError = false

    selected.load()
      .then((module) => {
        if (active) candidate = module.default
      })
      .catch(() => {
        if (active) loadError = true
      })

    return () => {
      active = false
    }
  })
</script>

<section class="p-6 lg:p-8" data-testid="icon-navigation-lab">
  <header class="mb-5">
    <h1 class="text-2xl font-semibold" style="color: var(--ds-text);">Icon navigation lab</h1>
    <p class="mt-1 max-w-3xl text-sm" style="color: var(--ds-text-subtle);">
      Compare icon families against the same interactive main and administration navigation. Candidate modules are lazy-loaded and belong only to the standalone design-system build.
    </p>
  </header>

  <div class="mb-6 flex flex-wrap gap-2" role="group" aria-label="Icon family">
    {#each candidates as option}
      <button
        class="rounded-full border px-3 py-1.5 text-sm font-medium transition-colors"
        style={selectedId === option.id
          ? 'border-color: #0c66e4; background: #0c66e4; color: white;'
          : 'border-color: var(--ds-border); background: var(--ds-surface-raised); color: var(--ds-text-subtle);'}
        data-testid="icon-navigation-candidate-{option.id}"
        onclick={() => (selectedId = option.id)}
      >
        {option.label}
      </button>
    {/each}
  </div>

  {#if candidate}
    {#key candidate.id}
      <NavigationMock {candidate} />
    {/key}
  {:else if loadError}
    <div class="rounded-lg border p-4 text-sm" style="border-color: var(--ds-border); color: var(--ds-text-danger);">
      This icon candidate could not be loaded.
    </div>
  {:else}
    <div class="grid h-48 place-items-center text-sm" style="color: var(--ds-text-subtle);">Loading icon candidate…</div>
  {/if}
</section>
