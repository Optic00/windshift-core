import { activityStore } from '../stores/activityStore.svelte.js';
import {
  canRunBackgroundSync,
  isExpectedBackgroundSyncError,
  onBackgroundSyncAvailable,
} from '../utils/backgroundSync.js';

const DEFAULT_ACTIVE = 30_000;
const DEFAULT_IDLE = 5 * 60_000;

/**
 * Adaptive polling composable. Calls fetchFn on an interval that shortens
 * when the user is active and stretches when idle / tab hidden.
 *
 * @param {() => Promise<void>|void} fetchFn
 * @param {{ active?: number, idle?: number, enabled?: () => boolean }} [opts]
 *   opts.enabled is a reactive predicate; while it returns false the timer is
 *   stopped (used to demote polling while an SSE stream is healthy, WI-484).
 */
export function usePoller(fetchFn, opts = {}) {
  const activeInterval = opts.active ?? DEFAULT_ACTIVE;
  const idleInterval = opts.idle ?? DEFAULT_IDLE;
  const enabled = opts.enabled ?? (() => true);

  let isPolling = $state(false);
  let lastPollTime = $state(null);
  let _timer = null;

  async function poll() {
    if (isPolling || !canRunBackgroundSync()) return;
    isPolling = true;
    try {
      await fetchFn();
      lastPollTime = Date.now();
    } catch (err) {
      if (!isExpectedBackgroundSyncError(err)) console.warn('usePoller: poll failed', err);
    } finally {
      isPolling = false;
    }
  }

  function _stopTimer() {
    if (_timer) {
      clearInterval(_timer);
      _timer = null;
    }
  }

  function _startTimer(interval) {
    _stopTimer();
    _timer = setInterval(poll, interval);
  }

  $effect(() => {
    const idle = activityStore.isIdle;
    // enabled() is read inside the effect so its reactive deps (e.g. SSE
    // connection state) re-run this block, stopping/starting the timer as the
    // stream connects or drops.
    if (!enabled()) {
      _stopTimer();
      return _stopTimer;
    }
    _startTimer(idle ? idleInterval : activeInterval);
    return _stopTimer;
  });

  $effect(() => {
    return onBackgroundSyncAvailable(() => {
      if (enabled()) void poll();
    });
  });

  return {
    poll,
    get isPolling() {
      return isPolling;
    },
    get lastPollTime() {
      return lastPollTime;
    },
  };
}
