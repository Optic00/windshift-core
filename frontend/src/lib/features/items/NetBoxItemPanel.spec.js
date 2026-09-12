/** @vitest-environment jsdom */
import '@testing-library/jest-dom/vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import NetBoxItemPanel from './NetBoxItemPanel.svelte';

const mocks = vi.hoisted(() => ({
  forWorkspace: vi.fn(),
  forItem: vi.fn(),
  search: vi.fn(),
  create: vi.fn(),
  refresh: vi.fn(),
  deleteLink: vi.fn(),
}));
vi.mock('../../api/netbox.js', () => ({
  netboxConnections: { forWorkspace: mocks.forWorkspace, search: mocks.search },
  netboxLinks: {
    forItem: mocks.forItem,
    create: mocks.create,
    refresh: mocks.refresh,
    delete: mocks.deleteLink,
  },
}));
vi.mock('../../stores/i18n.svelte.js', () => ({
  t: (key, values) => (values?.date ? `${key}:${values.date}` : key),
}));
const device = {
  object_type: 'dcim.device',
  object_id: 42,
  external_id: 'dcim.device:42',
  name: 'edge-router',
  url: 'https://netbox.test/dcim/devices/42/',
  status: 'Active',
};
const vm = {
  ...device,
  object_type: 'virtualization.virtualmachine',
  external_id: 'virtualization.virtualmachine:42',
  name: 'edge-vm',
  url: 'https://netbox.test/virtualization/virtual-machines/42/',
};
const link = {
  id: 'l1',
  connection_id: 'n1',
  connection_name: 'Primary',
  object: device,
  snapshot_updated_at: '2026-09-12T04:00:00Z',
  revision: 1,
};
describe('NetBoxItemPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.forWorkspace.mockResolvedValue([{ id: 'n1', name: 'Primary' }]);
    mocks.forItem.mockResolvedValue([link]);
    mocks.search.mockResolvedValue({ results: [device, vm], has_more: false, next_offset: null });
  });
  afterEach(cleanup);
  it('keeps same numeric IDs distinct by object type and links the selected snapshot identity', async () => {
    render(NetBoxItemPanel, { itemId: 7, workspaceId: 3, canEdit: true });
    await screen.findByText('edge-router');
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.linkObject' }));
    await fireEvent.input(screen.getByPlaceholderText('netbox.searchPlaceholder'), {
      target: { value: 'edge' },
    });
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.search' }));
    await screen.findByText('edge-vm');
    const buttons = screen.getAllByRole('button', { name: 'netbox.link' });
    mocks.create.mockResolvedValue({});
    mocks.forItem.mockResolvedValue([link]);
    await fireEvent.click(buttons[1]);
    await waitFor(() =>
      expect(mocks.create).toHaveBeenCalledWith(7, {
        connection_id: 'n1',
        object_type: 'virtualization.virtualmachine',
        object_id: 42,
      })
    );
  });
  it('hides edit controls for read-only viewers', async () => {
    render(NetBoxItemPanel, { itemId: 7, workspaceId: 3, canEdit: false });
    await screen.findByText('edge-router');
    expect(screen.queryByRole('button', { name: 'netbox.linkObject' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'netbox.refresh' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'netbox.unlink' })).not.toBeInTheDocument();
  });
  it('ignores a response from an old item context', async () => {
    const pending = Promise.withResolvers();
    mocks.forItem.mockReturnValueOnce(pending.promise);
    const view = render(NetBoxItemPanel, { itemId: 7, workspaceId: 3, canEdit: false });
    mocks.forItem.mockResolvedValue([]);
    await act(() => view.rerender({ itemId: 8, workspaceId: 4, canEdit: false }));
    pending.resolve([link]);
    await act(async () => {});
    expect(screen.queryByText('edge-router')).not.toBeInTheDocument();
  });

  it('allows a new search after query invalidation aborts an older search', async () => {
    const firstSearch = Promise.withResolvers();
    mocks.search.mockReturnValueOnce(firstSearch.promise).mockResolvedValueOnce({
      results: [vm],
      has_more: false,
      next_offset: null,
    });
    render(NetBoxItemPanel, { itemId: 7, workspaceId: 3, canEdit: true });
    await screen.findByText('edge-router');
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.linkObject' }));
    const searchInput = screen.getByPlaceholderText('netbox.searchPlaceholder');
    await fireEvent.input(searchInput, { target: { value: 'edge' } });
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.search' }));
    await waitFor(() => expect(mocks.search).toHaveBeenCalledTimes(1));
    await fireEvent.input(searchInput, { target: { value: 'virtual' } });
    expect(screen.getByRole('button', { name: 'netbox.search' })).not.toBeDisabled();
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.search' }));
    await screen.findByText('edge-vm');
    firstSearch.resolve({ results: [device], has_more: false, next_offset: null });
    await act(async () => {});
    expect(screen.queryByText('edge-router', { selector: '.divide-y *' })).not.toBeInTheDocument();
  });

  it('clears pending mutation state when the item context changes', async () => {
    const refresh = Promise.withResolvers();
    mocks.refresh.mockReturnValue(refresh.promise);
    const view = render(NetBoxItemPanel, { itemId: 7, workspaceId: 3, canEdit: true });
    await screen.findByText('edge-router');
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.refresh' }));
    mocks.forItem.mockResolvedValue([{ ...link, id: 'l2', object: vm }]);
    await act(() => view.rerender({ itemId: 8, workspaceId: 4, canEdit: true }));
    await screen.findByText('edge-vm');
    expect(screen.getByRole('button', { name: 'netbox.refresh' })).not.toBeDisabled();
    refresh.resolve(link);
  });

  it('hides the panel when no connection or link exists', async () => {
    mocks.forWorkspace.mockResolvedValue([]);
    mocks.forItem.mockResolvedValue([]);
    render(NetBoxItemPanel, { itemId: 7, workspaceId: 3, canEdit: true });
    await waitFor(() => expect(screen.queryByTestId('netbox-item-panel')).not.toBeInTheDocument());
  });

  it('serializes link mutations across overlay clicks', async () => {
    const refresh = Promise.withResolvers();
    mocks.refresh.mockReturnValue(refresh.promise);
    render(NetBoxItemPanel, { itemId: 7, workspaceId: 3, canEdit: true });
    await screen.findByText('edge-router');
    const refreshButton = screen.getByRole('button', { name: 'netbox.refresh' });
    await fireEvent.click(refreshButton);
    await fireEvent.click(refreshButton);
    expect(mocks.refresh).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('button', { name: 'netbox.unlink' })).toBeDisabled();
    refresh.resolve(link);
  });
});
