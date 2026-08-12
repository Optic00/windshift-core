<script>
  import { onDestroy, onMount } from 'svelte';
  import LazyMilkdownEditor from '../../editors/LazyMilkdownEditor.svelte';
  import { t } from '../../stores/i18n.svelte.js';

  let {
    content = '',
    title = 'Report',
    loading = false,
    error = '',
    loadingLabel = 'Preparing report for print…',
    backLabel = t('common.back'),
    onback = () => {},
    testId = 'markdown-print',
  } = $props();

  let printRootEl = $state(null);
  let autoPrinted = false;
  let savedColorMode = null;
  let savedTitle = null;

  function forceLightForPrint() {
    document.documentElement.dataset.colorMode = 'light';
  }

  function restoreColorMode() {
    const root = document.documentElement;
    if (savedColorMode === null) delete root.dataset.colorMode;
    else root.dataset.colorMode = savedColorMode;
  }

  onMount(() => {
    savedTitle = document.title;
    savedColorMode = document.documentElement.dataset.colorMode ?? null;
    forceLightForPrint();
    window.addEventListener('beforeprint', forceLightForPrint);
  });

  onDestroy(() => {
    window.removeEventListener('beforeprint', forceLightForPrint);
    restoreColorMode();
    if (savedTitle !== null) document.title = savedTitle;
  });

  $effect(() => {
    if (title) document.title = title;
  });

  $effect(() => {
    const expectedContent = content;
    if (loading || error || !expectedContent || autoPrinted) return;

    let cancelled = false;
    autoPrinted = true;
    waitForRenderReady(expectedContent).then(() => {
      if (cancelled) return;
      requestAnimationFrame(() => requestAnimationFrame(() => window.print()));
    });

    return () => {
      cancelled = true;
    };
  });

  function waitForRenderReady(expectedContent) {
    const maxWaitMs = 4000;
    const pollMs = 100;
    const start = performance.now();

    const domSettled = () => new Promise((resolve) => {
      const tick = () => {
        if (performance.now() - start >= maxWaitMs) return resolve();
        if (!printRootEl) return setTimeout(tick, pollMs);

        const editor = printRootEl.querySelector('[data-ready="true"]');
        const body = printRootEl.querySelector('.ProseMirror');
        const bodyReady = editor && body &&
          (!expectedContent.trim() || (body.textContent || '').trim().length > 0);
        const imagesReady = Array.from(printRootEl.querySelectorAll('img'))
          .every((image) => image.complete);

        if (bodyReady && imagesReady) return resolve();
        setTimeout(tick, pollMs);
      };
      tick();
    });

    const fontsReady = document.fonts?.ready?.catch(() => {}) ?? Promise.resolve();
    const timeout = new Promise((resolve) => setTimeout(resolve, maxWaitMs));
    return Promise.race([Promise.all([domSettled(), fontsReady]), timeout]);
  }
</script>

<main class="print-root" bind:this={printRootEl} data-testid={`${testId}-root`}>
  <div class="print-controls" role="toolbar" aria-label={t('common.print')}>
    <button
      type="button"
      class="print-button print-button--primary"
      onclick={() => window.print()}
      data-testid={`${testId}-button`}
    >
      {t('common.print')}
    </button>
    <button
      type="button"
      class="print-button"
      onclick={() => onback()}
      data-testid={`${testId}-back`}
    >
      {backLabel}
    </button>
  </div>

  {#if error}
    <p class="print-message print-error" role="alert" data-testid={`${testId}-error`}>{error}</p>
  {:else if loading}
    <p class="print-message" data-testid={`${testId}-loading`}>{loadingLabel}</p>
  {:else}
    <div class="print-body" data-testid={`${testId}-body`}>
      <LazyMilkdownEditor
        {content}
        readonly={true}
        showToolbar={false}
        testId={`${testId}-editor`}
      />
    </div>
  {/if}
</main>

<style>
  .print-root {
    box-sizing: border-box;
    width: min(100%, 60rem);
    min-height: 100vh;
    margin: 0 auto;
    padding: 2rem 1.5rem 4rem;
    background: var(--ds-surface);
    color: var(--ds-text);
  }

  .print-controls {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    margin-bottom: 1.5rem;
  }

  .print-button {
    min-height: 2.75rem;
    padding: 0.5rem 1rem;
    border: 1px solid var(--ds-border);
    border-radius: 0.375rem;
    background: var(--ds-surface-raised);
    color: var(--ds-text);
    font: inherit;
    font-size: 0.875rem;
    cursor: pointer;
  }

  .print-button:hover {
    background: var(--ds-background-neutral-hovered);
  }

  .print-button:focus-visible {
    outline: 2px solid var(--ds-border-focused);
    outline-offset: 2px;
  }

  .print-button--primary {
    border-color: var(--ds-interactive);
    background: var(--ds-interactive);
    color: var(--ds-text-inverse);
  }

  .print-button--primary:hover {
    background: var(--ds-interactive-hovered);
  }

  .print-message {
    color: var(--ds-text-subtle);
  }

  .print-error {
    color: var(--ds-text-danger);
  }

  :global(.print-body .milkdown-wrapper),
  :global(.print-body .milkdown-editor),
  :global(.print-body .milkdown-editor .milkdown),
  :global(.print-body .milkdown-editor .ProseMirror) {
    min-height: 0 !important;
    padding-right: 0 !important;
    padding-left: 0 !important;
    border: none !important;
    border-radius: 0 !important;
    background: transparent !important;
    box-shadow: none !important;
  }

  @media (max-width: 40rem) {
    .print-root {
      padding: 1rem 1rem 3rem;
    }

    :global(.print-body .milkdown-wrapper),
    :global(.print-body .milkdown-editor) {
      overflow: visible !important;
    }

    :global(.print-body .ProseMirror table) {
      table-layout: fixed !important;
      max-width: 100%;
    }

    :global(.print-body .ProseMirror th),
    :global(.print-body .ProseMirror td) {
      overflow-wrap: anywhere;
      word-break: break-word;
    }
  }

  @media print {
    @page {
      margin: 18mm 16mm;
    }

    :global(html),
    :global(body) {
      background: #fff !important;
      color: #111 !important;
      color-scheme: light !important;
    }

    .print-controls,
    :global(.milkdown-toolbar) {
      display: none !important;
    }

    .print-root {
      width: auto;
      min-height: 0;
      margin: 0;
      padding: 0;
      background: #fff !important;
      color: #111 !important;
    }

    :global(.ProseMirror h1),
    :global(.ProseMirror h2),
    :global(.ProseMirror h3),
    :global(.ProseMirror h4),
    :global(.ProseMirror h5),
    :global(.ProseMirror h6) {
      break-after: avoid;
      break-inside: avoid;
    }

    :global(.ProseMirror pre),
    :global(.ProseMirror blockquote),
    :global(.ProseMirror table),
    :global(.ProseMirror figure),
    :global(.ProseMirror img) {
      break-inside: avoid;
    }

    :global(.ProseMirror p),
    :global(.ProseMirror li) {
      orphans: 3;
      widows: 3;
    }

    :global(.ProseMirror thead) {
      display: table-header-group;
    }

    :global(.ProseMirror tr),
    :global(.ProseMirror td),
    :global(.ProseMirror th) {
      break-inside: avoid;
    }

    :global(.print-body .ProseMirror table) {
      table-layout: fixed !important;
      max-width: 100%;
    }

    :global(.print-body .ProseMirror td),
    :global(.print-body .ProseMirror th) {
      overflow-wrap: anywhere;
      word-break: break-word;
    }
  }
</style>
