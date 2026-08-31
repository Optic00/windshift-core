import { describe, expect, it } from 'vitest';
import {
  getWidgetMetadata,
  getWidgetMinWidth,
  widgetCategories,
} from '../../services/widgetRegistry.js';
import {
  getZammadObservedValueLabel,
  getZammadStatusBucketDisplayLabel,
  getZammadStatusBucketLabel,
  isCurrentZammadWorkspaceOverviewRequest,
} from '../../utils/zammadObservations.js';
import {
  isCurrentZammadMetadataRequest,
  isCurrentZammadPanelContext,
  isCurrentZammadTimelineRequest,
} from './zammadPanelContext.js';

describe('Zammad item panel context guards', () => {
  it('rejects a response from the previous item after a prop change', () => {
    expect(
      isCurrentZammadPanelContext(1, 2, 'old-item', 'new-item', 'workspace', 'workspace')
    ).toBe(false);
  });

  it('rejects a response from the previous workspace even when the item id is reused', () => {
    expect(
      isCurrentZammadPanelContext(3, 3, 'item', 'item', 'old-workspace', 'new-workspace')
    ).toBe(false);
  });

  it('accepts a response for the current item and workspace', () => {
    expect(isCurrentZammadPanelContext(4, 4, 'item', 'item', 'workspace', 'workspace')).toBe(true);
  });
});

describe('Zammad timeline request guards', () => {
  it('rejects a timeline response from a former item context', () => {
    expect(
      isCurrentZammadTimelineRequest(2, 2, 'old-item', 'new-item', 'workspace', 'workspace')
    ).toBe(false);
  });

  it('accepts a timeline response only for the current item and workspace', () => {
    expect(isCurrentZammadTimelineRequest(2, 2, 'item', 'item', 'workspace', 'workspace')).toBe(
      true
    );
  });
});

describe('Zammad support overview widget registration', () => {
  it('registers the workspace-wide support overview as a wide additional widget', () => {
    expect(getWidgetMetadata('zammad-support-overview')).toMatchObject({
      category: widgetCategories.ADDITIONAL,
      nameKey: 'workspaceDashboard.widgets.zammadSupportOverview.name',
      defaultWidth: 3,
      maxWidth: 3,
    });
    expect(getWidgetMinWidth('zammad-support-overview')).toBe(2);
  });
});

describe('Zammad observation labels', () => {
  const translate = (key, params = {}) => `${key}:${params.id ?? ''}`;

  it('uses an ID fallback for observed values with no display name', () => {
    expect(getZammadObservedValueLabel({ id: 17, name: ' ' }, translate)).toBe(
      'zammad.timeline.valueIdFallback:17'
    );
  });

  it('keeps an absent observed value distinct from an ID-only value', () => {
    expect(getZammadObservedValueLabel({ id: 0, name: '' }, translate)).toBe(
      'zammad.timeline.initialValue:'
    );
  });

  it('labels nameless status buckets with their ID or an unknown fallback', () => {
    expect(getZammadStatusBucketLabel({ id: 9, name: '' }, translate)).toBe(
      'zammad.overview.statusIdFallback:9'
    );
    expect(getZammadStatusBucketLabel({ id: null, name: '' }, translate)).toBe(
      'zammad.overview.unknownStatusBucket:'
    );
  });

  it('adds the readable connection name to a status bucket label', () => {
    const displayTranslate = (key, params = {}) =>
      key === 'zammad.overview.statusWithConnection'
        ? `${params.connection} · ${params.status}`
        : `${key}:${params.id ?? ''}`;
    expect(
      getZammadStatusBucketDisplayLabel(
        { id: 2, name: 'open', connection_name: 'Second helpdesk' },
        displayTranslate
      )
    ).toBe('Second helpdesk · open');
  });
});

describe('Zammad workspace overview request guards', () => {
  it('rejects a stale response for the same workspace after a newer refresh', () => {
    expect(isCurrentZammadWorkspaceOverviewRequest(1, 2, 7, 7)).toBe(false);
  });

  it('accepts matching versions even when the workspace ID changes representation', () => {
    expect(isCurrentZammadWorkspaceOverviewRequest(2, 2, 7, '7')).toBe(true);
  });
});

describe('Zammad metadata request guards', () => {
  it('rejects create metadata after switching to the link dialog', () => {
    expect(isCurrentZammadMetadataRequest(1, 1, 'connection', 'connection', true, 'link')).toBe(
      false
    );
  });

  it('rejects create metadata after the dialog closes', () => {
    expect(isCurrentZammadMetadataRequest(2, 2, 'connection', 'connection', false, 'create')).toBe(
      false
    );
  });

  it('accepts metadata only for the active create dialog and connection', () => {
    expect(isCurrentZammadMetadataRequest(3, 3, 'connection', 'connection', true, 'create')).toBe(
      true
    );
  });
});
