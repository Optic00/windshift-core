import { render, screen } from '@testing-library/svelte';
import { beforeEach, describe, expect, test, vi } from 'vitest';

vi.mock('../../api.js', () => ({
  api: {
    approvals: {
      forItem: vi.fn(),
      cancel: vi.fn(),
      decide: vi.fn(),
    },
  },
}));

vi.mock('../../stores/i18n.svelte.js', () => ({
  i18n: { locale: 'en' },
  t: vi.fn((key) => key),
}));
vi.mock('../../stores/toasts.svelte.js', () => ({
  errorToast: vi.fn(),
  successToast: vi.fn(),
}));
vi.mock('../../composables/useConfirm.js', () => ({ confirm: vi.fn() }));
vi.mock('../../stores', () => ({
  authStore: { currentUser: { id: 42 } },
}));

import { api } from '../../api.js';
import ApprovalsTimeline from './ApprovalsTimeline.svelte';

const pendingRequest = {
  id: 7,
  status: 'pending',
  triggered_by_user_id: 99,
  created_at: '2026-07-17T17:07:00Z',
  step_instances: [],
  decisions: [],
};

beforeEach(() => {
  vi.clearAllMocks();
  api.approvals.forItem.mockResolvedValue([pendingRequest]);
});

describe('ApprovalsTimeline cancellation', () => {
  test('shows cancellation to an item editor when another user opened the request', async () => {
    render(ApprovalsTimeline, { itemId: 12, canCancel: true });

    expect(await screen.findByText('Cancel approval request')).toBeTruthy();
  });

  test('does not show cancellation to a non-requestor without item edit permission', async () => {
    render(ApprovalsTimeline, { itemId: 12 });

    await screen.findByText('Approval #7');
    expect(screen.queryByText('Cancel approval request')).toBeNull();
  });

  test('explains an empty pool when the requestor may have been excluded from self-approval', async () => {
    api.approvals.forItem.mockResolvedValue([
      {
        ...pendingRequest,
        triggered_by_user_id: 42,
        step_instances: [
          {
            id: 8,
            display_order: 0,
            status: 'pending',
            started_at: '2026-07-17T17:07:00Z',
            approvers: [],
          },
        ],
      },
    ]);

    render(ApprovalsTimeline, { itemId: 12 });

    expect(await screen.findByText('No eligible approvers were resolved.')).toBeTruthy();
    expect(screen.getByText(/self-approval may be disabled/)).toBeTruthy();
  });
});
