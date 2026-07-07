import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api.js', () => ({ api: {} }));

const { screenEditorStore } = await import('./screenEditorStore.svelte.js');

describe('screenEditorStore required-field constraints', () => {
  beforeEach(() => {
    screenEditorStore.reset();
  });

  it('prevents enabling required for system fields create cannot satisfy', () => {
    screenEditorStore.screenFields = [
      { field_type: 'system', field_identifier: 'status', is_required: false },
    ];

    screenEditorStore.toggleFieldRequired(0);

    expect(screenEditorStore.screenFields[0].is_required).toBe(false);
  });

  it('allows clearing legacy invalid required flags', () => {
    screenEditorStore.screenFields = [
      { field_type: 'system', field_identifier: 'status', is_required: true },
    ];

    screenEditorStore.toggleFieldRequired(0);

    expect(screenEditorStore.screenFields[0].is_required).toBe(false);
  });

  it('allows required custom fields and renderable system fields', () => {
    screenEditorStore.screenFields = [
      { field_type: 'custom', field_identifier: '7', is_required: false },
      { field_type: 'system', field_identifier: 'story_points', is_required: false },
    ];

    screenEditorStore.toggleFieldRequired(0);
    screenEditorStore.toggleFieldRequired(1);

    expect(screenEditorStore.screenFields.map((field) => field.is_required)).toEqual([true, true]);
  });
});
