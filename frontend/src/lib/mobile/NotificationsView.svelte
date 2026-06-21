<script>
  import { onMount } from 'svelte';
  import { Bell, Check, BellRing, BellOff } from '@lucide/svelte';
  import { notifications, notificationActions } from '../stores/notifications.js';
  import { navigate } from '../router.js';
  import { getPushState, enablePush, disablePush, runPushDiagnostic } from './pushClient.js';
  import MobileHeader from './MobileHeader.svelte';

  let push = $state({ supported: false, installed: false, permission: 'default', subscribed: false });
  let pushBusy = $state(false);
  let testBusy = $state(false);
  // Diagnostic outcome shown inline under the toggle. `ok` drives the styling:
  // true = delivered, false = a problem to act on, null = neutral/idle.
  let testResult = $state(/** @type {{ok: boolean|null, detail: string}|null} */ (null));

  async function refreshPush() {
    push = await getPushState();
  }

  async function runDiagnostic() {
    if (testBusy) return;
    testBusy = true;
    testResult = null;
    try {
      const { verdict, detail } = await runPushDiagnostic();
      testResult = { ok: verdict === 'delivered' ? true : verdict === 'unsupported' ? null : false, detail };
    } finally {
      testBusy = false;
    }
  }

  async function togglePush() {
    if (pushBusy) return;
    pushBusy = true;
    try {
      if (push.subscribed) await disablePush();
      else await enablePush();
      await refreshPush();
    } finally {
      pushBusy = false;
    }
  }

  onMount(refreshPush);

  const list = $derived(
    [...$notifications].sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime())
  );
  const hasUnread = $derived($notifications.some((n) => !n.read));

  // Rewrite a desktop deep link to the mobile item-detail route when it points
  // at a work item; otherwise fall back to the original URL.
  function targetFor(actionUrl) {
    if (!actionUrl) return null;
    const m = actionUrl.match(/\/items\/(\d+)/);
    return m ? `/m/items/${m[1]}` : actionUrl;
  }

  async function open(n) {
    if (!n.read) notificationActions.markAsRead(n.id);
    const target = targetFor(n.actionUrl);
    if (target) navigate(target);
  }
</script>

<MobileHeader title="Notifications">
  {#snippet right()}
    {#if hasUnread}
      <button class="mark-all" onclick={() => notificationActions.markAllAsRead()} data-testid="notifications-mark-all" type="button">
        <Check size={16} /> Mark all read
      </button>
    {/if}
  {/snippet}
</MobileHeader>

{#if push.supported}
  <div class="push-banner" data-testid="push-banner">
    {#if !push.installed}
      <div class="pb-info">
        <BellOff size={18} />
        <span>Add Windshift to your Home Screen to enable push notifications.</span>
      </div>
    {:else if push.permission === 'denied'}
      <div class="pb-info">
        <BellOff size={18} />
        <span>Notifications are blocked. Enable them for Windshift in your device settings.</span>
      </div>
    {:else if push.subscribed}
      <button class="pb-btn on" onclick={togglePush} disabled={pushBusy} data-testid="push-toggle" type="button">
        <BellRing size={16} /> Notifications on
      </button>
      <button class="pb-test" onclick={runDiagnostic} disabled={testBusy} data-testid="push-test" type="button">
        {testBusy ? 'Sending test…' : 'Send test notification'}
      </button>
      {#if testResult}
        <p
          class="pb-result"
          class:ok={testResult.ok === true}
          class:bad={testResult.ok === false}
          data-testid="push-test-result"
        >
          {testResult.detail}
        </p>
      {/if}
    {:else}
      <button class="pb-btn" onclick={togglePush} disabled={pushBusy} data-testid="push-toggle" type="button">
        <Bell size={16} /> Enable notifications
      </button>
    {/if}
  </div>
{/if}

<div class="list" data-testid="notifications-list">
  {#if list.length === 0}
    <div class="empty" data-testid="notifications-empty">
      <Bell size={28} />
      <p>You're all caught up</p>
    </div>
  {:else}
    {#each list as n (n.id)}
      <button class="n-row" class:unread={!n.read} onclick={() => open(n)} data-testid="notification-row" type="button">
        {#if !n.read}<span class="dot" aria-label="Unread"></span>{/if}
        <div class="n-body">
          <span class="n-title">{n.title}</span>
          {#if n.message}<span class="n-msg">{n.message}</span>{/if}
          <span class="n-time">{notificationActions.formatTimestamp(n.timestamp)}</span>
        </div>
      </button>
    {/each}
  {/if}
</div>

<style>
  .mark-all {
    display: inline-flex; align-items: center; gap: 0.25rem;
    padding: 0.35rem 0.6rem; border: none; background: transparent;
    color: var(--ds-text-link, var(--ds-interactive)); font-size: 0.8125rem; cursor: pointer;
  }

  .n-row {
    width: 100%; display: flex; align-items: flex-start; gap: 0.6rem;
    padding: 0.85rem 0.75rem; text-align: left; background-color: var(--ds-surface);
    border: none; border-bottom: 1px solid var(--ds-border); cursor: pointer; min-height: 56px;
  }
  .n-row.unread { background-color: color-mix(in srgb, var(--ds-interactive) 6%, var(--ds-surface)); }
  .n-row:active { background-color: var(--ds-background-neutral-hovered); }

  .dot { flex-shrink: 0; margin-top: 6px; width: 8px; height: 8px; border-radius: var(--radius-full, 9999px); background-color: var(--ds-interactive); }

  .n-body { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .n-title { font-size: 0.9375rem; font-weight: var(--font-medium, 500); color: var(--ds-text); }
  .n-msg { font-size: 0.8125rem; color: var(--ds-text-subtle); }
  .n-time { font-size: 0.6875rem; color: var(--ds-text-subtlest, var(--ds-text-subtle)); margin-top: 2px; }

  .empty { display: flex; flex-direction: column; align-items: center; gap: 0.5rem; padding: 3rem 1rem; color: var(--ds-text-subtle); }
  .empty p { margin: 0; font-size: 0.9375rem; }

  .push-banner { padding: 0.75rem; border-bottom: 1px solid var(--ds-border); }
  .pb-info { display: flex; align-items: center; gap: 0.6rem; font-size: 0.8125rem; color: var(--ds-text-subtle); }
  .pb-btn {
    width: 100%; display: inline-flex; align-items: center; justify-content: center; gap: 0.4rem;
    min-height: 44px; border: 1px solid var(--ds-interactive); border-radius: var(--radius-lg, 8px);
    background-color: var(--ds-interactive); color: var(--ds-text-inverse, #fff);
    font-size: 0.875rem; font-weight: var(--font-semibold, 600); cursor: pointer;
  }
  .pb-btn.on { background-color: var(--ds-surface); color: var(--ds-interactive); }
  .pb-btn:disabled { opacity: 0.6; }

  .pb-test {
    width: 100%; min-height: 40px; margin-top: 0.5rem;
    border: none; background: transparent;
    color: var(--ds-text-link, var(--ds-interactive));
    font-size: 0.8125rem; cursor: pointer;
  }
  .pb-test:disabled { opacity: 0.6; }

  .pb-result {
    margin: 0.5rem 0 0; padding: 0.5rem 0.6rem;
    border-radius: var(--radius-md, 6px);
    font-size: 0.8125rem; line-height: 1.35;
    background-color: var(--ds-background-neutral, color-mix(in srgb, var(--ds-text) 6%, var(--ds-surface)));
    color: var(--ds-text-subtle);
  }
  .pb-result.ok {
    background-color: color-mix(in srgb, var(--ds-success, #16a34a) 12%, var(--ds-surface));
    color: var(--ds-text);
  }
  .pb-result.bad {
    background-color: color-mix(in srgb, var(--ds-danger, #dc2626) 12%, var(--ds-surface));
    color: var(--ds-text);
  }
</style>
