import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api.js', () => ({
  api: {
    items: {
      update: vi.fn(),
      transition: vi.fn(),
    },
  },
}));

const { api } = await import('../api.js');
const { itemDetailStore } = await import('./itemDetailStore.svelte.js');

describe('itemDetailStore.saveField', () => {
  beforeEach(() => {
    itemDetailStore.item = { id: 42, estimate_minutes: null, story_points: null };
    itemDetailStore.saving = false;
    itemDetailStore.hasChanges = false;
    api.items.update.mockReset();
    api.items.update.mockImplementation(async (_id, data) => ({ ...data }));
  });

  it('persists estimate_minutes updates', async () => {
    await itemDetailStore.saveField('estimate_minutes', 240);

    expect(api.items.update).toHaveBeenCalledWith(42, { estimate_minutes: 240 });
    expect(itemDetailStore.item.estimate_minutes).toBe(240);
    expect(itemDetailStore.hasChanges).toBe(true);
  });

  it('persists estimate clears', async () => {
    itemDetailStore.item = { id: 42, estimate_minutes: 240 };

    await itemDetailStore.saveField('estimate_minutes', null);

    expect(api.items.update).toHaveBeenCalledWith(42, { estimate_minutes: null });
    expect(itemDetailStore.item.estimate_minutes).toBeNull();
  });
});
