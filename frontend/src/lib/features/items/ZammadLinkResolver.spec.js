/** @vitest-environment jsdom */

import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ZammadSupportWidget from '../../widgets/ZammadSupportWidget.svelte';
import ZammadLinkResolver from './ZammadLinkResolver.svelte';

const mocks = vi.hoisted(() => ({
  resolve: vi.fn(),
  workspaceOverview: vi.fn(),
  navigate: vi.fn(),
}));

vi.mock('../../api.js', () => ({
  api: {
    zammadTickets: {
      resolve: mocks.resolve,
      workspaceOverview: mocks.workspaceOverview,
    },
  },
}));
vi.mock('../../router.js', () => ({ navigate: mocks.navigate }));
vi.mock('../../stores/i18n.svelte.js', () => ({
  t: (key, params = {}) => {
    if (key === 'zammad.overview.statusWithConnection') {
      return `${params.connection} · ${params.status}`;
    }
    if (key === 'zammad.ticketNumber') return `Zammad #${params.number}`;
    return key;
  },
}));
vi.mock('../../stores', () => ({ authStore: { currentUser: { timezone: 'UTC' } } }));

describe('ZammadLinkResolver', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it('replaces the resolver route with the current item destination', async () => {
    mocks.resolve.mockResolvedValue({ workspace_id: 7, item_id: 42 });

    render(ZammadLinkResolver, { correlationKey: 'windshift%3Aprovider%3ATST-42' });

    await waitFor(() =>
      expect(mocks.navigate).toHaveBeenCalledWith('/workspaces/7/items/42', { replace: true })
    );
    expect(mocks.resolve).toHaveBeenCalledWith('windshift:provider:TST-42');
  });

  it('keeps a retry action when the link cannot be resolved', async () => {
    mocks.resolve.mockRejectedValueOnce(new Error('not found'));
    mocks.resolve.mockResolvedValueOnce({ workspace_id: 7, item_id: 42 });

    render(ZammadLinkResolver, { correlationKey: 'windshift:provider:TST-42' });

    const retry = await screen.findByRole('button', { name: 'common.retry' });
    expect(screen.getByText('zammad.returnLinkFailed')).toBeInTheDocument();
    await fireEvent.click(retry);
    await waitFor(() => expect(mocks.navigate).toHaveBeenCalledOnce());
  });

  it('renders the workspace-wide support widget with actionable ticket context', async () => {
    mocks.workspaceOverview.mockResolvedValue({
      total: 4,
      active: 2,
      closed: 1,
      unassigned: 1,
      sync_failed: 1,
      creation_uncertain: 0,
      unknown_status: 0,
      by_status: [
        {
          connection_id: 'connection-1',
          connection_name: 'Primary helpdesk',
          id: 3,
          name: 'open',
          count: 1,
          closed: false,
        },
        {
          connection_id: 'connection-2',
          connection_name: 'Second helpdesk',
          id: 3,
          name: 'open',
          count: 1,
          closed: false,
        },
      ],
      tickets: [
        {
          id: 'link-42',
          item_id: 42,
          item_key: 'OPS-42',
          ticket_title: 'VPN access after password change',
          ticket_number: '12345',
          ticket_url: 'https://zammad.example.test/#ticket/zoom/42',
          status: { id: 2, name: 'open' },
          group: { id: 7, name: 'IT / Network' },
          owner: { id: 99, name: 'Grace Hopper' },
        },
        {
          id: 'link-44',
          item_id: 44,
          item_key: 'OPS-44',
          ticket_title: 'Printer queue unavailable',
          ticket_number: '12347',
          ticket_url: 'https://zammad.example.test/#ticket/zoom/44',
          status: { id: 4, name: 'pending reminder' },
          group: { id: 8, name: 'IT / Workplace' },
          owner: { id: 1, name: '-' },
        },
      ],
      recent_changes: [
        {
          id: 'event-1',
          item_id: 42,
          item_key: 'OPS-42',
          ticket_title: 'VPN access after password change',
          ticket_number: '12345',
          ticket_url: 'https://zammad.example.test/#ticket/zoom/42',
          current_group: { id: 7, name: 'IT / Network' },
          current_owner: { id: 99, name: 'Grace Hopper' },
          field: 'status',
          old_value: { id: 1, name: 'new' },
          new_value: { id: 2, name: 'open' },
          observed_at: '2026-08-31T10:00:00Z',
        },
        {
          id: 'event-2',
          item_id: 43,
          item_key: 'OPS-43',
          ticket_number: '12346',
          field: 'priority',
          observed_at: '2026-08-31T11:00:00Z',
        },
      ],
    });

    render(ZammadSupportWidget, { workspaceId: 7 });

    await waitFor(() => expect(mocks.workspaceOverview).toHaveBeenCalledWith(7, { limit: 5 }));
    expect(await screen.findByTestId('zammad-support-overview')).toBeInTheDocument();
    expect(
      screen.getByRole('link', { name: /OPS-42 VPN access after password change/ })
    ).toHaveAttribute('href', '/workspaces/7/items/42');
    const zammadLink = screen.getAllByRole('link', { name: /Zammad #12345/ })[0];
    expect(zammadLink).toHaveAttribute('href', 'https://zammad.example.test/#ticket/zoom/42');
    expect(zammadLink).toHaveAttribute('target', '_blank');
    expect(zammadLink).toHaveAttribute('rel', 'noopener noreferrer');
    expect(screen.getAllByText('IT / Network').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Grace Hopper').length).toBeGreaterThan(0);
    expect(screen.getByRole('link', { name: /Printer queue unavailable/ })).toHaveAttribute(
      'href',
      'https://zammad.example.test/#ticket/zoom/44'
    );
    expect(screen.getByRole('link', { name: 'OPS-44' })).toHaveAttribute(
      'href',
      '/workspaces/7/items/44'
    );
    expect(screen.getByText('IT / Workplace')).toBeInTheDocument();
    expect(screen.getByText('zammad.unassignedOwner')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'OPS-43' })).not.toBeInTheDocument();
    expect(screen.getByText('Primary helpdesk · open: 1')).toBeInTheDocument();
    expect(screen.getByText('Second helpdesk · open: 1')).toBeInTheDocument();
    expect(screen.queryByText(/article|customer/i)).not.toBeInTheDocument();
  });

  it('refreshes the workspace-wide overview on demand', async () => {
    mocks.workspaceOverview.mockResolvedValue({
      total: 1,
      active: 1,
      closed: 0,
      unassigned: 0,
      sync_failed: 0,
      creation_uncertain: 0,
      unknown_status: 0,
      by_status: [],
      recent_changes: [],
    });

    render(ZammadSupportWidget, { workspaceId: 7 });

    await screen.findByTestId('zammad-support-overview');
    await fireEvent.click(screen.getByRole('button', { name: 'zammad.overview.refresh' }));

    await waitFor(() => expect(mocks.workspaceOverview).toHaveBeenCalledTimes(2));
    expect(mocks.workspaceOverview).toHaveBeenLastCalledWith(7, { limit: 5 });
  });

  it('refreshes the overview at the synchronization cadence and clears the interval on unmount', async () => {
    vi.useFakeTimers();
    mocks.workspaceOverview.mockResolvedValue({
      total: 1,
      active: 1,
      closed: 0,
      unassigned: 0,
      sync_failed: 0,
      creation_uncertain: 0,
      unknown_status: 0,
      by_status: [],
      recent_changes: [],
    });

    const { unmount } = render(ZammadSupportWidget, { workspaceId: 7 });
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.workspaceOverview).toHaveBeenCalledOnce();

    await vi.advanceTimersByTimeAsync(120_000);
    expect(mocks.workspaceOverview).toHaveBeenCalledTimes(2);

    unmount();
    await vi.advanceTimersByTimeAsync(120_000);
    expect(mocks.workspaceOverview).toHaveBeenCalledTimes(2);
  });
});
