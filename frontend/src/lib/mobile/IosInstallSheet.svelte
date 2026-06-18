<script>
  import { Share, SquarePlus } from '@lucide/svelte';
  import Modal from '../dialogs/Modal.svelte';
  import { iosInstallOpen } from './installClient.js';

  function close() {
    iosInstallOpen.set(false);
  }
</script>

<Modal bind:isOpen={$iosInstallOpen} maxWidth="max-w-sm" zIndexClass="z-[600]" onclose={close}>
  <div class="sheet" data-testid="ios-install-sheet">
    <h2>Add to Home Screen</h2>
    <p class="lead">Install Windshift as an app to launch it full-screen and receive push notifications.</p>
    <ol class="steps">
      <li><span class="n">1</span> Open this page in <strong>Safari</strong> (not inside another app).</li>
      <li><span class="n">2</span> Tap the <strong>Share</strong> button <Share size={15} /> in the toolbar.</li>
      <li><span class="n">3</span> Choose <strong>Add to Home Screen</strong> <SquarePlus size={15} />.</li>
      <li><span class="n">4</span> Tap <strong>Add</strong>, then open Windshift from the new icon.</li>
    </ol>
    <button class="ok" onclick={close} data-testid="ios-install-dismiss" type="button">Got it</button>
  </div>
</Modal>

<style>
  .sheet { display: flex; flex-direction: column; gap: 0.85rem; padding: 1.1rem; }
  h2 { margin: 0; font-size: 1.0625rem; font-weight: var(--font-semibold, 600); color: var(--ds-text); }
  .lead { margin: 0; font-size: 0.875rem; color: var(--ds-text-subtle); line-height: 1.5; }
  .steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.7rem; }
  .steps li {
    display: flex; align-items: center; flex-wrap: wrap; gap: 0.4rem;
    font-size: 0.9375rem; color: var(--ds-text); line-height: 1.4;
  }
  .n {
    flex-shrink: 0; width: 22px; height: 22px; display: inline-flex; align-items: center; justify-content: center;
    border-radius: var(--radius-full, 9999px); background-color: var(--ds-background-neutral);
    color: var(--ds-text-subtle); font-size: 0.75rem; font-weight: var(--font-semibold, 600);
  }
  .ok {
    margin-top: 0.25rem; min-height: 44px; border: none; border-radius: var(--radius-md, 6px);
    background-color: var(--ds-interactive); color: var(--ds-text-inverse, #fff);
    font-weight: var(--font-semibold, 600); cursor: pointer;
  }
</style>
