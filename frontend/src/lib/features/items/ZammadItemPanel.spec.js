/** @vitest-environment jsdom */

import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ZammadItemPanel from './ZammadItemPanel.svelte';

const mocks = vi.hoisted(() => ({
  connections: vi.fn(),
  links: vi.fn(),
  history: vi.fn(),
  refresh: vi.fn(),
  link: vi.fn(),
  successToast: vi.fn(),
  errorToast: vi.fn(),
}));

function linkedTicket(overrides = {}) {
  return {
    id: 'link-1',
    connection_id: 'zammad-dev',
    connection_name: 'Zammad Dev',
    ticket_id: 42,
    ticket_number: '34007',
    ticket_title: '[NST-1] MFA device for test account reset',
    ticket_url: 'https://zammad.example.test/#ticket/zoom/42',
    group_id: 7,
    group_name: 'Windshift',
    owner_id: 1,
    owner_name: '',
    sync_state: 'linked',
    last_status_id: 4,
    last_status_name: 'closed',
    closed: true,
    last_synced_at: '2026-08-31T13:10:00Z',
    ...overrides,
  };
}

vi.mock('../../api.js', () => ({
  api: {
    zammadConnections: {
      forWorkspace: mocks.connections,
    },
    zammadTickets: {
      forItem: mocks.links,
      history: mocks.history,
      refresh: mocks.refresh,
      link: mocks.link,
    },
  },
}));
vi.mock('../../stores/toasts.svelte.js', () => ({
  successToast: mocks.successToast,
  errorToast: mocks.errorToast,
}));
vi.mock('../../stores/i18n.svelte.js', () => ({
  t: (key, params = {}) => {
    if (key === 'zammad.ticketNumber') return `Zammad #${params.number}`;
    if (key === 'zammad.lastSynced') return `Synced ${params.time}`;
    if (key === 'zammad.overview.statusIdFallback') return `Status ID ${params.id}`;
    return key;
  },
}));
vi.mock('../../stores', () => ({ authStore: { currentUser: { timezone: 'UTC' } } }));

describe('ZammadItemPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    Object.defineProperty(Element.prototype, 'animate', {
      configurable: true,
      value: vi.fn(() => ({
        cancel: vi.fn(),
        currentTime: 0,
        effect: null,
        playState: 'finished',
      })),
    });
    mocks.connections.mockResolvedValue([
      { id: 'zammad-dev', name: 'Zammad Dev', auth_method: 'api_token', ready: true },
    ]);
    mocks.links.mockResolvedValue([linkedTicket()]);
    mocks.history.mockResolvedValue({ events: [] });
    mocks.refresh.mockResolvedValue(linkedTicket());
    mocks.link.mockResolvedValue(linkedTicket());
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it('renders a linked ticket with a clear title, semantic status, and compact metadata', async () => {
    render(ZammadItemPanel, { itemId: 5, workspaceId: 3, canEdit: false });

    await waitFor(() => expect(mocks.links).toHaveBeenCalledWith(5));
    const card = await screen.findByTestId('zammad-ticket-card-link-1');
    const ticketLink = within(card).getByRole('link', {
      name: '[NST-1] MFA device for test account reset',
    });
    expect(ticketLink).toHaveAttribute('href', 'https://zammad.example.test/#ticket/zoom/42');
    expect(ticketLink).toHaveAttribute('target', '_blank');
    expect(ticketLink).toHaveAttribute('rel', 'noopener noreferrer');
    expect(within(card).getByText('Zammad #34007')).toBeInTheDocument();
    expect(within(card).getByText('Zammad Dev')).toBeInTheDocument();
    expect(within(card).getByText('Windshift')).toBeInTheDocument();
    expect(within(card).getByText('zammad.unassignedOwner')).toBeInTheDocument();

    const closed = within(card).getByText('closed');
    expect(closed.style.borderColor).toBe('rgb(34, 197, 94)');
    expect(within(card).queryByText('zammad.status: closed')).not.toBeInTheDocument();

    const synced = card.querySelector('time');
    expect(synced).toHaveAttribute('datetime', '2026-08-31T13:10:00Z');
    expect(synced).toHaveAttribute('title');
  });

  it('keeps creation and ticket maintenance actions in compact menus', async () => {
    render(ZammadItemPanel, { itemId: 5, workspaceId: 3, canEdit: true });

    await screen.findByTestId('zammad-ticket-card-link-1');
    const add = screen.getByRole('button', { name: 'common.add' });
    expect(screen.queryByRole('menuitem', { name: 'zammad.createTicket' })).not.toBeInTheDocument();
    await fireEvent.click(add);
    expect(
      await screen.findByRole('menuitem', { name: 'zammad.linkExistingTicket' })
    ).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'zammad.createTicket' })).toBeInTheDocument();

    await fireEvent.click(add);
    await fireEvent.click(screen.getByRole('button', { name: 'common.actions' }));
    expect(await screen.findByRole('menuitem', { name: 'zammad.editTicket' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'zammad.refreshTicket' })).toBeInTheDocument();
    expect(screen.getByRole('menuitem', { name: 'zammad.removeTicketLink' })).toBeInTheDocument();
  });

  it('uses the status ID fallback instead of the link sync state when the status name is missing', async () => {
    mocks.links.mockResolvedValue([
      linkedTicket({ last_status_id: 9, last_status_name: '', closed: false }),
    ]);

    render(ZammadItemPanel, { itemId: 5, workspaceId: 3, canEdit: false });

    const card = await screen.findByTestId('zammad-ticket-card-link-1');
    expect(within(card).getByText('Status ID 9')).toBeInTheDocument();
    expect(within(card).queryByText('zammad.syncState.linked')).not.toBeInTheDocument();
  });

  it('keeps an observed status neutral until the closed-state projection is known', async () => {
    mocks.links.mockResolvedValue([linkedTicket({ closed: undefined })]);

    render(ZammadItemPanel, { itemId: 5, workspaceId: 3, canEdit: false });

    const card = await screen.findByTestId('zammad-ticket-card-link-1');
    expect(within(card).getByText('closed').style.borderColor).toBe('rgb(113, 113, 122)');
  });

  it('keeps the existing card visible when a successful refresh cannot reload ticket data', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    mocks.links
      .mockResolvedValueOnce([linkedTicket()])
      .mockRejectedValueOnce(new Error('reload failed'));

    render(ZammadItemPanel, { itemId: 5, workspaceId: 3, canEdit: true });

    const card = await screen.findByTestId('zammad-ticket-card-link-1');
    await fireEvent.click(within(card).getByRole('button', { name: 'common.actions' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'zammad.refreshTicket' }));

    await waitFor(() => expect(mocks.refresh).toHaveBeenCalledWith('link-1'));
    await waitFor(() =>
      expect(mocks.errorToast).toHaveBeenCalledWith('zammad.ticketReloadAfterChangeFailed')
    );
    expect(screen.getByTestId('zammad-ticket-card-link-1')).toBeInTheDocument();
    expect(screen.getByText('[NST-1] MFA device for test account reset')).toBeInTheDocument();
    expect(screen.queryByText('zammad.loadLinksFailed')).not.toBeInTheDocument();
    expect(mocks.successToast).not.toHaveBeenCalled();
    expect(consoleError).toHaveBeenCalledWith('Failed to load Zammad links:', expect.any(Error));
  });

  it('does not report a reload failure when a newer SSE reload supersedes the mutation reload', async () => {
    /** @type {(value: unknown) => void} */
    let resolveMutationReload = () => {};
    const mutationReload = new Promise((resolve) => {
      resolveMutationReload = resolve;
    });
    mocks.links
      .mockResolvedValueOnce([linkedTicket()])
      .mockImplementationOnce(() => mutationReload)
      .mockResolvedValueOnce([linkedTicket({ last_synced_at: '2026-08-31T13:20:00Z' })]);

    render(ZammadItemPanel, { itemId: 5, workspaceId: 3, canEdit: true });

    const card = await screen.findByTestId('zammad-ticket-card-link-1');
    await fireEvent.click(within(card).getByRole('button', { name: 'common.actions' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'zammad.refreshTicket' }));
    await waitFor(() => expect(mocks.links).toHaveBeenCalledTimes(2));

    window.dispatchEvent(new CustomEvent('item-zammad-links-changed', { detail: { itemId: 5 } }));
    await waitFor(() => expect(mocks.links).toHaveBeenCalledTimes(3));
    resolveMutationReload([linkedTicket()]);

    await waitFor(() => expect(mocks.successToast).toHaveBeenCalledWith('zammad.ticketRefreshed'));
    expect(mocks.errorToast).not.toHaveBeenCalledWith('zammad.ticketReloadAfterChangeFailed');
  });

  it('does not reuse a closed classification when a link fallback reports a new status', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    mocks.links
      .mockResolvedValueOnce([linkedTicket()])
      .mockRejectedValueOnce(new Error('reload failed'));
    mocks.link.mockResolvedValue({
      ...linkedTicket({ last_status_id: 2, last_status_name: 'open' }),
      ticket_title: undefined,
      closed: undefined,
    });

    render(ZammadItemPanel, { itemId: 5, workspaceId: 3, canEdit: true });

    await screen.findByTestId('zammad-ticket-card-link-1');
    await fireEvent.click(screen.getByRole('button', { name: 'common.add' }));
    await fireEvent.click(
      await screen.findByRole('menuitem', { name: 'zammad.linkExistingTicket' })
    );
    await fireEvent.input(screen.getByPlaceholderText('zammad.ticketNumberPlaceholder'), {
      target: { value: '34007' },
    });
    await fireEvent.click(screen.getByRole('button', { name: 'zammad.linkExistingTicket' }));

    await waitFor(() =>
      expect(mocks.errorToast).toHaveBeenCalledWith('zammad.ticketReloadAfterChangeFailed')
    );
    const updatedCard = screen.getByTestId('zammad-ticket-card-link-1');
    expect(
      within(updatedCard).getByText('[NST-1] MFA device for test account reset')
    ).toBeInTheDocument();
    expect(within(updatedCard).getByText('open').style.borderColor).toBe('rgb(113, 113, 122)');
    expect(consoleError).toHaveBeenCalledWith('Failed to load Zammad links:', expect.any(Error));
  });
});
