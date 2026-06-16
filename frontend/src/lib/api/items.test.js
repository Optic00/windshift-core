import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

/**
 * Pin that the mutating `items.*` API methods broadcast a cross-tab freshness
 * notice to other open tabs on success (and only on success). The notice
 * itself is exercised by crossTabSync.test.js; here we only assert that the
 * wrapper wires `notifyItemMutation` into the post-success path and does not
 * alter the request/response shape.
 */

class FakeBroadcastChannel {
  static instances = new Set();
  #listeners = new Set();
  constructor() {
    FakeBroadcastChannel.instances.add(this);
  }
  postMessage(data) {
    for (const ch of FakeBroadcastChannel.instances) {
      if (ch === this) continue;
      for (const cb of ch.#listeners) cb({ data });
    }
  }
  addEventListener(_t, cb) {
    this.#listeners.add(cb);
  }
  removeEventListener(_t, cb) {
    this.#listeners.delete(cb);
  }
  close() {
    this.#listeners.clear();
    FakeBroadcastChannel.instances.delete(this);
  }
}

let posted = [];

describe('items API cross-tab broadcast', () => {
  let fetchSpy;

  beforeEach(() => {
    vi.resetModules();
    FakeBroadcastChannel.instances.clear();
    posted = [];
    vi.stubGlobal('BroadcastChannel', FakeBroadcastChannel);

    fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: 42, title: 'x' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json', Date: new Date().toUTCString() },
      })
    );
    vi.stubGlobal('fetch', fetchSpy);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  // Create a peer channel that captures everything the items client broadcasts.
  async function captureBroadcasts() {
    const mod = await import('./items.js');
    const peer = new FakeBroadcastChannel();
    peer.addEventListener('message', (e) => posted.push(e.data));
    return { mod, peer };
  }

  it('create broadcasts a notice with the new item id from the response', async () => {
    const { mod, peer } = await captureBroadcasts();
    const result = await mod.items.create({ title: 'x' });
    expect(result.id).toBe(42);
    expect(posted).toEqual([expect.objectContaining({ type: 'create', itemId: 42 })]);
    peer.close();
  });

  it('update broadcasts with the id arg', async () => {
    const { mod, peer } = await captureBroadcasts();
    await mod.items.update(7, { title: 'y' });
    expect(posted[0]).toEqual(expect.objectContaining({ type: 'update', itemId: 7 }));
    peer.close();
  });

  it('transition broadcasts with the id arg', async () => {
    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify({ item: { id: 9, status_id: 2 } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json', Date: new Date().toUTCString() },
      })
    );
    const { mod, peer } = await captureBroadcasts();
    const item = await mod.items.transition(9, 2);
    expect(item.id).toBe(9);
    expect(posted[0]).toEqual(expect.objectContaining({ type: 'transition', itemId: 9 }));
    peer.close();
  });

  it('updateFracIndex broadcasts a reorder notice', async () => {
    const { mod, peer } = await captureBroadcasts();
    await mod.items.updateFracIndex(3, { frac_index: 'a0' });
    expect(posted[0]).toEqual(expect.objectContaining({ type: 'reorder', itemId: 3 }));
    peer.close();
  });

  it('does not broadcast when the request rejects', async () => {
    fetchSpy.mockResolvedValueOnce(
      new Response(JSON.stringify({ error: 'boom' }), {
        status: 500,
        headers: { 'Content-Type': 'application/json', Date: new Date().toUTCString() },
      })
    );
    const { mod, peer } = await captureBroadcasts();
    await expect(mod.items.update(1, { title: 'z' })).rejects.toBeDefined();
    expect(posted).toEqual([]);
    peer.close();
  });
});
