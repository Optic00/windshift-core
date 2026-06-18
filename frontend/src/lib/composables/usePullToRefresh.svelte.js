/**
 * Pull-to-refresh composable for the mobile scroll surface.
 *
 * Wires touch listeners onto a scroll container so the user can pull the
 * content down (from the top of the scroll area) to trigger a manual reload —
 * the native "pull to refresh" gesture. Returns reactive state the host can
 * bind to a spinner/arrow indicator.
 *
 * Behaviour:
 * - Only engages when the container is scrolled to the very top (scrollTop
 *   <= 0), so mid-scroll dragging stays a normal scroll.
 * - Resistance: the pull distance is dampened (1 / 2 past the start) so the
 *   content never drags far below the gesture — feels like iOS/Material.
 * - Fires `onRefresh` once a configurable threshold is crossed on release;
 *   smaller drags animate back without firing.
 * - Guarded against re-entrancy: while a refresh is in flight, new pulls are
 *   ignored until it resolves.
 *
 * Call inside an `$effect` (Svelte 5 runes) so listeners attach/detach with
 * the host component's lifecycle and re-bind if the target element changes.
 *
 * @param {() => HTMLElement|null} getTarget
 *   Resolves the scroll container to attach listeners to (null = not mounted yet).
 * @param {() => Promise<void>|void} onRefresh
 *   Called when the user releases a pull past `threshold`.
 * @param {{ threshold?: number, maxPull?: number, resistance?: number }} [opts]
 *   threshold (px) to trigger — default 64; maxPull (px) visual cap — default 96.
 * @returns {{
 *   pulling: boolean,
 *   pullDistance: number,
 *   refreshing: boolean,
 *   threshold: number,
 * }}
 */
export function usePullToRefresh(getTarget, onRefresh, opts = {}) {
  const threshold = opts.threshold ?? 64;
  const maxPull = opts.maxPull ?? 96;
  const resistance = opts.resistance ?? 2;

  let pulling = $state(false);
  let pullDistance = $state(0);
  let refreshing = $state(false);

  // Raw drag bookkeeping (not reactive — no need to re-render on every move).
  let startY = 0;
  let active = false;

  function resistanceOffset(raw) {
    if (raw <= 0) return 0;
    // Dampen beyond the start so a fast flick can't slam the content far down.
    return raw / resistance;
  }

  function clamp(value, max) {
    return Math.min(Math.max(value, 0), max);
  }

  function onTouchStart(e) {
    if (refreshing) return;
    // Pulling only begins at the top of the scroll surface.
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
