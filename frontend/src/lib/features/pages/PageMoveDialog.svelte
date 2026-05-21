<script>
  import { tick } from 'svelte';
  import Modal from '../../dialogs/Modal.svelte';
  import { api } from '../../api.js';

  /**
   * "Move to…" dialog for reparenting a page. Computes valid destinations
   * by excluding the page itself and any descendant — the backend cycle
   * check is the source of truth, but filtering up front gives the user a
   * cleaner picker and avoids a guaranteed-error round-trip.
   *
   * Descendant detection uses the materialized path returned by
   * /pages/tree, so it survives without an extra request per row.
   */
  let {
    isOpen = $bindable(false),
    workspaceId,
    page,
    onMoved = null,
  } = $props();

  let candidates = $state([]);
  let filter = $state('');
  let loading = $state(false);
  let saving = $state(false);
  let error = $state('');
  /** @type {HTMLInputElement | null} */
  let filterEl = $state(null);

  $effect(() => {
    if (isOpen && page) {
      loadCandidates();
      // Focus the filter input after the modal mounts. Using a programmatic
      // focus rather than the `autofocus` HTML attribute avoids the
      // a11y-autofocus warning while still landing the cursor where the
      // user expects when the dialog opens.
      tick().then(() => filterEl?.focus());
    }
    if (!isOpen) {
      filter = '';
      error = '';
    }
  });

  async function loadCandidates() {
    loading = true;
    error = '';
    try {
      const resp = await api.pages.getTree(workspaceId);
      const all = resp.pages || [];
      // A page p2 is a descendant of `page` iff its materialized path
      // starts with the path that the children of `page` would have.
      // Schema path format: "/a/b/c/" — descendants of page id N at path
      // "/X/N/" have paths starting with "/X/N/".
      const selfPrefix = `${page.path}${page.id}/`;
      candidates = all.filter((p) => {
        if (p.id === page.id) return false;
        if (p.path === selfPrefix || p.path.startsWith(selfPrefix)) return false;
        return true;
      });
    } catch (err) {
      error = err?.message || 'Failed to load pages';
    } finally {
      loading = false;
    }
  }

  const filtered = $derived(
    !filter
      ? candidates
      : candidates.filter((p) => p.title.toLowerCase().includes(filter.toLowerCase()))
  );

  async function moveTo(parentId) {
    saving = true;
    error = '';
    try {
      await api.pages.movePage(workspaceId, page.id, parentId);
      isOpen = false;
      onMoved?.();
    } catch (err) {
      error = err?.message || 'Move failed';
    } finally {
      saving = false;
    }
  }
</script>

<Modal bind:isOpen maxWidth="max-w-lg">
  <div class="dialog">
    <header>
      <h2>Move "{page?.title || ''}"</h2>
      <p class="hint">Choose a new parent. Pages under the current page are hidden because they would create a cycle.</p>
    </header>

    {#if error}
      <div class="error" role="alert">{error}</div>
    {/if}

    {#if loading}
      <p class="status">Loading…</p>
    {:else}
      <input
        bind:this={filterEl}
        id="page-move-filter"
        class="filter"
        type="text"
        placeholder="Search pages…"
        bind:value={filter}
      />

      <ul class="results" data-testid="page-move-results">
        <li>
          <button
            id="page-move-to-root"
            type="button"
            class="result"
            disabled={saving || page?.parent_id == null}
            onclick={() => moveTo(null)}
          >
            <span class="path">/</span>
            <span>Workspace root</span>
          </button>
        </li>
        {#each filtered as candidate (candidate.id)}
          <li>
            <button
              type="button"
              class="result"
              data-testid="page-move-candidate"
              disabled={saving || candidate.id === page?.parent_id}
              onclick={() => moveTo(candidate.id)}
            >
              <span class="path">{candidate.path || '/'}</span>
              <span>{candidate.title}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}

    <footer>
      <button type="button" onclick={() => (isOpen = false)}>Cancel</button>
    </footer>
  </div>
</Modal>

<style>
  .dialog {
    padding: 1rem 1.25rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  h2 {
    margin: 0;
    font-size: 1.0625rem;
    font-weight: 600;
  }

  .hint {
    margin: 0.25rem 0 0 0;
    font-size: 0.8125rem;
    color: var(--ds-text-subtle, #6b7280);
  }

  .filter {
    padding: 0.375rem 0.5rem;
    font-size: 0.875rem;
    border: 1px solid var(--ds-border, #d1d5db);
    border-radius: 0.25rem;
  }

  .results {
    list-style: none;
    padding: 0;
    margin: 0;
    max-height: 320px;
    overflow-y: auto;
    border: 1px solid var(--ds-border, #e5e7eb);
    border-radius: 0.25rem;
  }

  .result {
    width: 100%;
    text-align: left;
    background: transparent;
    border: none;
    padding: 0.5rem 0.75rem;
    cursor: pointer;
    display: flex;
    flex-direction: column;
    gap: 0.125rem;
    border-bottom: 1px solid var(--ds-border, #f3f4f6);
  }

  .result:hover:not(:disabled) {
    background: var(--ds-background-neutral-hovered, #f3f4f6);
  }

  .result:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .path {
    font-size: 0.75rem;
    color: var(--ds-text-subtle, #6b7280);
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  }

  footer {
    display: flex;
    justify-content: flex-end;
    border-top: 1px solid var(--ds-border, #e5e7eb);
    padding-top: 0.5rem;
  }

  footer button {
    padding: 0.375rem 0.75rem;
    font-size: 0.875rem;
    border: 1px solid var(--ds-border, #d1d5db);
    border-radius: 0.25rem;
    background: var(--ds-background, #fff);
    cursor: pointer;
  }

  .error {
    padding: 0.625rem 0.875rem;
    background: var(--ds-background-danger, #fef2f2);
    color: var(--ds-text-danger, #b91c1c);
    border-radius: 0.25rem;
    font-size: 0.875rem;
  }

  .status {
    color: var(--ds-text-subtle, #6b7280);
    font-size: 0.875rem;
  }
</style>
