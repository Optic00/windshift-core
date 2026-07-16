import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api.js', () => ({
  api: {
    links: { getForItems: vi.fn() },
  },
}));

const { api } = await import('../api.js');
const { CollectionFieldLinksStore } = await import('./collectionFieldLinks.svelte.js');

describe('CollectionFieldLinksStore', () => {
  let store;

  beforeEach(() => {
    vi.clearAllMocks();
    store = new CollectionFieldLinksStore();
    api.links.getForItems.mockImplementation(async (ids) =>
      Object.fromEntries(ids.map((id) => [id, { outgoing: [], incoming: [] }]))
    );
  });

  it('hydrates a visible page with one custom-field-aware batch request', async () => {
    await store.loadForItems(Array.from({ length: 100 }, (_, index) => index + 1));

    expect(api.links.getForItems).toHaveBeenCalledTimes(1);
    expect(api.links.getForItems).toHaveBeenCalledWith(
      Array.from({ length: 100 }, (_, index) => index + 1),
      { includeCustomFields: true }
    );
  });

  it('chunks large pages and caches empty results', async () => {
    const ids = Array.from({ length: 401 }, (_, index) => index + 1);
    await store.loadForItems(ids);
    await store.loadForItems(ids);

    expect(api.links.getForItems).toHaveBeenCalledTimes(3);
    expect(api.links.getForItems.mock.calls.map(([chunk]) => chunk.length)).toEqual([200, 200, 1]);
  });

  it('selects primary and mirror links for the requested field and item', async () => {
    api.links.getForItems.mockResolvedValue({
      10: {
        outgoing: [
          { id: 1, custom_field_id: 7, source_type: 'item', source_id: 10, target_id: 20 },
          { id: 2, custom_field_id: 8, source_type: 'item', source_id: 10, target_id: 30 },
        ],
        incoming: [
          { id: 3, custom_field_id: 7, source_id: 40, target_type: 'item', target_id: 10 },
        ],
      },
    });

    await store.loadForItems([10]);

    expect(store.getFieldLinks(10, 7)).toEqual([expect.objectContaining({ id: 1 })]);
    expect(store.getFieldLinks(10, 99, '{"mirror_of_field_id":7}')).toEqual([
      expect.objectContaining({ id: 3 }),
    ]);
  });

  it('does not restore a previous account response after reset', async () => {
    let resolveOldRequest;
    api.links.getForItems.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveOldRequest = resolve;
      })
    );

    const oldLoad = store.loadForItems([10]);
    store.reset();
    resolveOldRequest({
      10: {
        outgoing: [{ id: 1, custom_field_id: 7, source_type: 'item', source_id: 10 }],
        incoming: [],
      },
    });
    await oldLoad;

    expect(store.getFieldLinks(10, 7)).toEqual([]);
  });
});
