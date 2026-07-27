/**
 * Adds pull-to-refresh to a scroll container. It engages only at scroll top,
 * damps and caps the gesture, and ignores pulls while refresh is in flight.
 * Call from an $effect so listeners follow the target lifecycle.
 *
 * @param {() => HTMLElement|null} getTarget
 * @param {() => Promise<void>|void} onRefresh
 * @param {{ threshold?: number, maxPull?: number, resistance?: number }} [opts]
 * @returns {{ pulling: boolean, pullDistance: number, refreshing: boolean, threshold: number }}
 */
export function usePullToRefresh(getTarget, onRefresh, opts = {}) {
  const threshold = opts.threshold ?? 64;
  const maxPull = opts.maxPull ?? 96;
  const resistance = opts.resistance ?? 2;

  let pulling = $state(false);
  let pullDistance = $state(0);
  let refreshing = $state(false);

  // Drag bookkeeping is intentionally non-reactive.
  let startY = 0;
  let active = false;

  function resistanceOffset(raw) {
    if (raw <= 0) return 0;
    // Dampen fast pulls.
    return raw / resistance;
  }

  function clamp(value, max) {
    return Math.min(Math.max(value, 0), max);
  }

  function onTouchStart(e) {
    if (refreshing) return;
    // Only pull from the top.
    const el = getTarget();
    if (!el || el.scrollTop > 0) {
      active = false;
      return;
    }
    const touch = e.touches[0];
    startY = touch.clientY;
    active = true;
  }

  function onTouchMove(e) {
    if (!active || refreshing) return;
    const touch = e.touches[0];
    const delta = touch.clientY - startY;
    if (delta <= 0) {
      // Dragging up — reset and let the browser scroll normally.
      if (pulling) pulling = false;
      pullDistance = 0;
      return;
    }
    // The container is at the top and the drag is downward: claim the gesture
    // so the page doesn't also rubber-band-scroll, and translate the content.
    if (e.cancelable) e.preventDefault();
    pulling = true;
    pullDistance = clamp(resistanceOffset(delta), maxPull);
  }

  async function onTouchEnd() {
    if (!active) return;
    active = false;
    if (!pulling) return;
    pulling = false;
    if (pullDistance >= threshold && !refreshing) {
      refreshing = true;
      pullDistance = threshold;
      try {
        await onRefresh();
      } finally {
        refreshing = false;
        pullDistance = 0;
      }
    } else {
      // Under threshold — snap back without firing.
      pullDistance = 0;
    }
  }

  function onTouchCancel() {
    active = false;
    pulling = false;
    pullDistance = 0;
  }

  $effect(() => {
    const el = getTarget();
    if (!el) return;
    // passive:false so preventDefault can stop the native overscroll on pull.
    el.addEventListener('touchstart', onTouchStart, { passive: true });
    el.addEventListener('touchmove', onTouchMove, { passive: false });
    el.addEventListener('touchend', onTouchEnd, { passive: true });
    el.addEventListener('touchcancel', onTouchCancel, { passive: true });
    return () => {
      el.removeEventListener('touchstart', onTouchStart);
      el.removeEventListener('touchmove', onTouchMove);
      el.removeEventListener('touchend', onTouchEnd);
      el.removeEventListener('touchcancel', onTouchCancel);
    };
  });

  return {
    get pulling() {
      return pulling;
    },
    get pullDistance() {
      return pullDistance;
    },
    get refreshing() {
      return refreshing;
    },
    get threshold() {
      return threshold;
    },
  };
}
