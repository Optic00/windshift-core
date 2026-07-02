import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api.js', () => ({
  api: {
    items: {
      getChildren: vi.fn(),
      update: vi.fn(),
      transition: vi.fn(),
    },
  },
}));

const { api } = await import('../api.js');
const { itemDetailStore } = await import('./itemDetailStore.svelte.js');

describe('itemDetailStore.loadChildItems', () => {
  beforeEach(() => {
    itemDetailStore.itemId = 42;
    itemDetailStore.childItems = [];
    itemDetailStore.loadingChildItems = false;
    api.items.getChildren.mockReset();
  });

  it('keeps the existing child item array when fetched summary data is unchanged', async () => {
    const currentChildren = [
      {
        id: 7,
        workspace_id: 2,
        workspace_key: 'WI',
        workspace_item_number: 101,
        item_type_id: 5,
        title: 'Child item',
        status_id: 1,
        status_name: 'Open',
        status_color: '#94a3b8',
        frac_index: 'a0',
        description: 'local expanded data should not matter',
      },
    ];
    itemDetailStore.childItems = currentChildren;
    const currentRef = itemDetailStore.childItems;
    api.items.getChildren.mockResolvedValue([
      {
        id: 7,
        workspace_id: 2,
        workspace_key: 'WI',
        workspace_item_number: 101,
        item_type_id: 5,
        title: 'Child item',
        status_id: 1,
        status_name: 'Open',
        status_color: '#94a3b8',
        frac_index: 'a0',
      },
    ]);

    await itemDetailStore.loadChildItems();

    expect(itemDetailStore.childItems).toBe(currentRef);
  });

  it('replaces child items when display-relevant data changes', async () => {
    const currentChildren = [{ id: 7, title: 'Old title' }];
    const nextChildren = [{ id: 7, title: 'New title' }];
    itemDetailStore.childItems = currentChildren;
    const currentRef = itemDetailStore.childItems;
    api.items.getChildren.mockResolvedValue({ items: nextChildren });

    await itemDetailStore.loadChildItems();

    expect(itemDetailStore.childItems).not.toBe(currentRef);
    expect(itemDetailStore.childItems).toEqual(nextChildren);
  });
});

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
