/**
 * Serialize page saves while retaining the newest queued snapshot for each
 * page. Navigation can enqueue another page without discarding either save.
 *
 * @param {(snapshot: any) => Promise<void>} save
 * @param {(left: any, right: any) => boolean} [isSame]
 */
export function createPageAutosaveQueue(save, isSame = Object.is) {
  const pending = new Map();
  const active = new Map();
  let draining = false;
  let drainPromise = Promise.resolve();

  async function drain() {
    try {
      while (pending.size > 0) {
        const next = pending.entries().next().value;
        if (!next) break;

        const [key, snapshot] = next;
        pending.delete(key);
        active.set(key, snapshot);
        try {
          await save(snapshot);
        } finally {
          active.delete(key);
        }
      }
    } finally {
      draining = false;
    }
  }

  return {
    enqueue(key, snapshot) {
      if (active.has(key) && isSame(active.get(key), snapshot)) {
        return drainPromise;
      }
      if (pending.has(key) && isSame(pending.get(key), snapshot)) {
        return drainPromise;
      }

      pending.set(key, snapshot);
      if (!draining) {
        draining = true;
        drainPromise = drain();
      }
      return drainPromise;
    },
  };
}
