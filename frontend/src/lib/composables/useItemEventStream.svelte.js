import { toExternal } from '../runtime/contextPath.js';
import { itemLiveUpdates } from '../stores/itemLiveUpdates.svelte.js';

const DEBOUNCE_MS = 250;

/**
 * Subscribe to one item's server-sent event stream and dispatch targeted
 * reloads (WI-484). Opens an EventSource to /items/{id}/events (built through
 * the context-path helper, since EventSource is not one of the globals the
 * frontend translation layer patches) and maps each event `kind` to a handler.
 *
 * Event-kind → action matrix:
 *   connected/reload → onReconcile  (full reload; covers every section,
 *                                    including ones with no granular event:
 *                                    attachments, diagrams, worklogs, SCM links)
 *   status           → onItem       (item record + transitions)
 *   updated/created  → onItem + onChildren (own fields, and child list since a
 *                                    parent is published "updated" on child change)
 *   comment          → onComment
 *   link             → onLinks      (no granular link reload → typically a full reload)
 *   deleted          → onDeleted
 *
 * Events are coalesced over a short debounce so a burst (e.g. a status change
 * that emits several events) triggers each reload once. A `reconcile` in the
 * batch supersedes the targeted reloads.
 *
 * On connect AND on every auto-reconnect the server re-sends `connected`, so a
 * full reconcile always runs after a reconnect — nothing missed while
 * disconnected is left stale. While disconnected, `connected` is false so the
 * components' pollers resume as the fallback.
 *
 * @param {() => (number|string|null|undefined)} getItemId reactive item id
 * @param {{ onReconcile?: Function, onItem?: Function, onChildren?: Function, onComment?: Function, onLinks?: Function, onDeleted?: Function }} handlers
 * @returns {{ readonly connected: boolean }}
 */
export function useItemEventStream(getItemId, handlers = {}) {
  let connected = $state(false);

  $effect(() => {
    const itemId = getItemId();
    if (!itemId) return;
    if (typeof EventSource === 'undefined') return; // SSR / unsupported → polling stays the source of truth

    const pending = new Set();
    let timer = null;

    const flush = () => {
      timer = null;
      const kinds = new Set(pending);
      pending.clear();
      // A full reconcile reloads everything, so skip the narrower reloads.
      if (kinds.has('reconcile')) {
        handlers.onReconcile?.();
        return;
      }
      if (kinds.has('item')) handlers.onItem?.();
      if (kinds.has('children')) handlers.onChildren?.();
      if (kinds.has('comment')) handlers.onComment?.();
      if (kinds.has('links')) handlers.onLinks?.();
      if (kinds.has('deleted')) handlers.onDeleted?.();
    };
    const schedule = (...kinds) => {
      for (const k of kinds) pending.add(k);
      if (!timer) timer = setTimeout(flush, DEBOUNCE_MS);
    };

    const es = new EventSource(toExternal(`/api/items/${itemId}/events`));
    const setConnected = (value) => {
      connected = value;
      itemLiveUpdates.set(itemId, value);
    };

    es.addEventListener('connected', () => {
      setConnected(true);
      schedule('reconcile');
    });
    es.addEventListener('reload', () => schedule('reconcile'));
    es.addEventListener('comment', () => schedule('comment'));
    es.addEventListener('status', () => schedule('item'));
    es.addEventListener('updated', () => schedule('item', 'children'));
    es.addEventListener('created', () => schedule('item', 'children'));
    es.addEventListener('deleted', () => schedule('deleted'));
    es.addEventListener('link', () => schedule('links'));
    // The browser auto-reconnects (honoring the server's retry hint). Until it
    // does, mark disconnected so the components' pollers resume as the fallback.
    es.onerror = () => setConnected(false);

    return () => {
      if (timer) clearTimeout(timer);
      es.close();
      itemLiveUpdates.clear(itemId);
      connected = false;
    };
  });

  return {
    get connected() {
      return connected;
    },
  };
}
