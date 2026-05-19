import { BUCKET } from '../buckets.js';
import { createCommand } from '../types.js';

function dispatchCreateModal(type, workspaceId) {
  window.dispatchEvent(
    new CustomEvent('show-create-modal', {
      detail: {
        type,
        workspaceId: workspaceId ? Number(workspaceId) : undefined,
      },
    })
  );
}

/**
 * Global "Create X" actions. When inside a workspace, the create modal
 * auto-selects the current workspace (existing behavior preserved).
 */
export function createProvider(ctx) {
  const { t, workspaceId } = ctx;

  const make = (id, createType, labelKey, descriptionKey, keywords) =>
    createCommand({
      id,
      label: t(labelKey),
      description: t(descriptionKey),
      bucket: BUCKET.CREATE,
      keywords,
      execute: () => dispatchCreateModal(createType, workspaceId),
    });

  return [
    make(
      'create-work-item',
      'work-item',
      'commandPalette.commands.createWorkItem.label',
      'commandPalette.commands.createWorkItem.description',
      ['create', 'new', 'work', 'item', 'task', 'issue']
    ),
    make(
      'create-workspace',
      'workspace',
      'commandPalette.commands.createWorkspace.label',
      'commandPalette.commands.createWorkspace.description',
      ['create', 'new', 'workspace', 'project', 'space']
    ),
    make(
      'create-milestone',
      'milestone',
      'commandPalette.commands.createMilestone.label',
      'commandPalette.commands.createMilestone.description',
      ['create', 'new', 'milestone', 'target', 'deadline']
    ),
    make(
      'create-collection',
      'collection',
      'commandPalette.commands.createCollection.label',
      'commandPalette.commands.createCollection.description',
      ['create', 'new', 'collection', 'group']
    ),
  ];
}
