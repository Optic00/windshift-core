<script>
  import { onMount } from 'svelte';
  import { Plus } from '@lucide/svelte';
  import { currentRoute } from '../router.js';
  import { timerStore } from '../stores/timerStore.svelte.js';
  import { workspacesStore } from '../stores';
  import { startNotificationPoller } from '../stores/notifications.js';
  import { registerMobileServiceWorker } from './pushClient.js';
  import MobileNav from './MobileNav.svelte';
  import GlobalConfirmDialog from '../dialogs/GlobalConfirmDialog.svelte';
  import MyWorkView from './MyWorkView.svelte';
  import PersonalView from './PersonalView.svelte';
  import TimerView from './TimerView.svelte';
  import NotificationsView from './NotificationsView.svelte';
  import MobileItemDetail from './MobileItemDetail.svelte';
  import SearchView from './SearchView.svelte';
  import MobileCreateDialog from './MobileCreateDialog.svelte';

  const view = $derived($currentRoute.view);
  const TAB_VIEWS = ['mobile-my-work', 'mobile-personal', 'mobile-timer', 'mobile-notifications'];
  const isTabView = $derived(TAB_VIEWS.includes(view));
  let createOpen = $state(false);

  onMount(() => {
    // Reuse the same singletons the desktop shell drives, so the active timer
    // and notification inbox stay live on the phone surface too.
    timerStore.initialize();
    startNotificationPoller();
    registerMobileServiceWorker();
    // MainApp normally loads this; the mobile shell bypasses MainApp, so load
    // it here for the create dialog's workspace list (store guards re-loads).
    workspacesStore.load();
  });
</script>

<div class="mobile-shell" data-testid="mobile-shell">
  <main class="mobile-scroll">
    {#if view === 'mobile-my-work'}
      <MyWorkView />
    {:else if view === 'mobile-personal'}
      <PersonalView />
    {:else if view === 'mobile-timer'}
      <TimerView />
    {:else if view === 'mobile-notifications'}
      <NotificationsView />
    {:else if view === 'mobile-search'}
      <SearchView />
    {:else if view === 'mobile-item-detail'}
      <MobileItemDetail itemId={Number($currentRoute.params.id)} />
    {/if}
  </main>

  {#if isTabView}
    <button class="fab" onclick={() => (createOpen = true)} data-testid="mobile-create-fab" aria-label="Create item" type="button">
      <Plus size={26} />
    </button>
  {/if}

  {#if view !== 'mobile-item-detail' && view !== 'mobile-search'}
    <MobileNav />
  {/if}
</div>

<!-- Global confirm host (the mobile shell bypasses MainApp, which normally
     mounts this) so confirm() dialogs render on the mobile surface. -->
<GlobalConfirmDialog />

<!-- Simple create dialog, reachable from the FAB on any tab. -->
<MobileCreateDialog bind:isOpen={createOpen} />

<style>
  .mobile-shell {
    display: flex;
    flex-direction: column;
    height: 100dvh;
    width: 100%;
    background-color: var(--ds-surface);
    color: var(--ds-text);
    overflow: hidden;
  }

  .mobile-scroll {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    -webkit-overflow-scrolling: touch;
    /* Clear the fixed bottom nav + iPhone home indicator. */
    padding-bottom: calc(env(safe-area-inset-bottom, 0px) + 4rem);
  }

  .fab {
    position: fixed;
    right: 1rem;
    /* Sit just above the bottom nav + home indicator. */
    bottom: calc(env(safe-area-inset-bottom, 0px) + 4.5rem);
    z-index: 40;
    width: 52px;
    height: 52px;
    display: flex;
    align-items: center;
    justify-content: center;
    border: none;
    border-radius: var(--radius-full, 9999px);
    background-color: var(--ds-interactive);
    color: var(--ds-text-inverse, #fff);
    box-shadow: var(--shadow-float, 0 6px 16px rgba(0, 0, 0, 0.28));
    cursor: pointer;
  }
  .fab:active { background-color: var(--ds-interactive-pressed, var(--ds-interactive-hovered, var(--ds-interactive))); }
</style>
