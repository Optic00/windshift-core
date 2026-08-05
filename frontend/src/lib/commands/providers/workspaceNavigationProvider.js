import {
  testNavigationItems,
  workspaceOnlyViews,
  workspaceViewItems,
} from '../../navigation/workspaceNavigation.js';
import { workspacePermissions } from '../../stores';
import { BUCKET } from '../buckets.js';
import { createCommand } from '../types.js';

function buildViewUrl(workspaceId, view, collectionId) {
  const prefix = collectionId ? `/collections/${collectionId}` : '';
  return `/workspaces/${workspaceId}${prefix}/${view}`;
}

/** Workspace navigation for every workspace route, without the overview
 * dashboard keyword that polluted board searches. */
export function workspaceNavigationProvider(ctx) {
  const { workspaceId, workspace, collectionId, route, modules } = ctx;
  if (!workspaceId) return [];

  const name = workspace?.name || 'Workspace';
  const collectionSuffix = collectionId ? ' in this collection' : '';
  const out = [];

  out.push(
    createCommand({
      id: 'workspace-overview',
      label: `${name} Overview`,
      description: collectionId ? 'Open this collection overview' : 'Open workspace overview',
      bucket: BUCKET.WORKSPACE_NAVIGATION,
      keywords: ['overview', 'workspace', 'stats', name.toLowerCase()],
      url: collectionId
        ? buildViewUrl(workspaceId, 'overview', collectionId)
        : `/workspaces/${workspaceId}`,
    })
  );

  for (const view of workspaceViewItems) {
    out.push(
      createCommand({
        id: `workspace-${view.id}-view`,
        label: `Open ${name} ${view.label}`,
        description: `Switch to ${view.label.toLowerCase()} view${collectionSuffix}`,
        bucket: BUCKET.WORKSPACE_NAVIGATION,
        keywords: [view.id, view.label.toLowerCase(), name.toLowerCase()],
        url: buildViewUrl(workspaceId, view.id, collectionId),
      })
    );
  }

  if (!collectionId) {
    for (const view of workspaceOnlyViews) {
      if (view.id === 'agents' && !workspacePermissions.canAdminWorkspace(workspaceId)) continue;
      out.push(
        createCommand({
          id: `workspace-${view.id}-view`,
          label: `Open ${name} ${view.label}`,
          description: view.tooltip || '',
          bucket: BUCKET.WORKSPACE_NAVIGATION,
          keywords: [view.id, view.label.toLowerCase(), name.toLowerCase()],
          url: buildViewUrl(workspaceId, view.id, null),
        })
      );
    }
  }

  if (
    modules?.test_management_enabled &&
    workspacePermissions.canViewTests(workspaceId) &&
    !collectionId
  ) {
    for (const view of testNavigationItems) {
      const slug = view.id === 'test-cases' ? 'tests' : `tests/${view.id.replace(/^test-/, '')}`;
      out.push(
        createCommand({
          id: `workspace-${view.id}`,
          label: `${name} ${view.label}`,
          description: view.tooltip || '',
          bucket: BUCKET.WORKSPACE_NAVIGATION,
          keywords: ['test', 'testing', 'qa', view.id, view.label.toLowerCase()],
          url: `/workspaces/${workspaceId}/${slug}`,
        })
      );
    }
  }

  // Filter out the command for the current view to reduce noise.
  const here = `${route?.path || ''}${typeof window !== 'undefined' ? window.location.search : ''}`;
  return out.filter((c) => c.url !== here);
}
