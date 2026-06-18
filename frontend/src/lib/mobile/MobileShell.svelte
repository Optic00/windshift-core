<script>
  import { onMount } from 'svelte';
  import { currentRoute } from '../router.js';
  import { timerStore } from '../stores/timerStore.svelte.js';
  import { startNotificationPoller } from '../stores/notifications.js';
  import { registerMobileServiceWorker } from './pushClient.js';
  import MobileNav from './MobileNav.svelte';
  import MyWorkView from './MyWorkView.svelte';
  import PersonalView from './PersonalView.svelte';
  import TimerView from './TimerView.svelte';
  import NotificationsView from './NotificationsView.svelte';
  import MobileItemDetail from './MobileItemDetail.svelte';

  const view = $derived($currentRoute.view);

  onMount(() => {
    // Reuse the same singletons the desktop shell drives, so the active timer
    // and notification inbox stay live on the phone surface too.
    timerStore.initialize();
    startNotificationPoller();
    registerMobileServiceWorker();
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
    {:else if view === 'mobile-item-detail'}
      <MobileItemDetail itemId={Number($currentRoute.params.id)} />
    {/if}
  </main>

  {#if view !== 'mobile-item-detail'}
    <MobileNav />
  {/if}
</div>

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
</style>
