import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../api.js', () => ({ api: {} }));

const { workItemFormStore } = await import('./workItemFormStore.svelte.js');

describe('workItemFormStore screen-field validation', () => {
  beforeEach(() => {
    workItemFormStore.reset();
    workItemFormStore.selectedWorkspace = { id: 2 };
    workItemFormStore.formData.workspace_id = 2;
    workItemFormStore.formData.name = 'Test item';
    workItemFormStore.formData.item_type_id = 5;
  });

  it('validates required milestone against milestone_ids', () => {
    workItemFormStore.screenFields = [
      {
        field_type: 'system',
        field_identifier: 'milestone',
        is_required: true,
      },
    ];

    expect(workItemFormStore.validate()).toBe(false);
    expect(workItemFormStore.validationErrors).toEqual(['Milestone is required']);

    workItemFormStore.formData.milestone_ids = [10];

    expect(workItemFormStore.validate()).toBe(true);
  });

  it('does not block create for auto-managed required system fields', () => {
    workItemFormStore.screenFields = [
      { field_type: 'system', field_identifier: 'status', is_required: true },
      { field_type: 'system', field_identifier: 'created_at', is_required: true },
    ];

    expect(workItemFormStore.validate()).toBe(true);
  });

  it('validates required labels and exposes them for post-create assignment', () => {
    workItemFormStore.screenFields = [
      { field_type: 'system', field_identifier: 'labels', is_required: true },
    ];

    expect(workItemFormStore.validate()).toBe(false);
    expect(workItemFormStore.validationErrors).toEqual(['Labels is required']);

    workItemFormStore.selectedPersonalLabels = [{ id: 5, name: 'UI' }];

    expect(workItemFormStore.validate()).toBe(true);
    expect(workItemFormStore.getFormData().personal_label_ids).toEqual([5]);
  });

  it('submits newly renderable system fields in the create payload', () => {
    workItemFormStore.formData.iteration_id = 12;
    workItemFormStore.formData.project_id = 34;
    workItemFormStore.formData.story_points = '5.5';
    workItemFormStore.formData.estimate = '1d 2h 30m';

    expect(workItemFormStore.getFormData()).toMatchObject({
      iteration_id: 12,
      project_id: 34,
      story_points: 5.5,
      estimate_minutes: 630,
    });
  });
});
