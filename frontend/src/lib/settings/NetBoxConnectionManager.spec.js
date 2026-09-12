/** @vitest-environment jsdom */
import '@testing-library/jest-dom/vitest';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import NetBoxConnectionManager from './NetBoxConnectionManager.svelte';

const mocks = vi.hoisted(() => ({
  getAll: vi.fn(),
  get: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  test: vi.fn(),
  getWorkspaces: vi.fn(),
}));
vi.mock('../api/netbox.js', () => ({
  netboxConnections: {
    getAll: mocks.getAll,
    get: mocks.get,
    create: mocks.create,
    update: mocks.update,
    test: mocks.test,
    delete: vi.fn(),
  },
}));
vi.mock('../api.js', () => ({ api: { workspaces: { getAll: mocks.getWorkspaces } } }));
vi.mock('../stores/i18n.svelte.js', () => ({ t: (key) => key }));
vi.mock('../stores/toasts.svelte.js', () => ({
  successToast: vi.fn(),
  errorToast: vi.fn(),
  warningToast: vi.fn(),
}));
vi.mock('../composables/useConfirm.js', () => ({ confirm: vi.fn() }));

const connection = {
  id: 'n1',
  revision: 7,
  slug: 'primary',
  name: 'Primary',
  enabled: true,
  base_url: 'https://netbox.test',
  auth_scheme: 'bearer',
  applies_to_all_workspaces: false,
  workspace_ids: [3],
};
describe('NetBoxConnectionManager', () => {
  beforeAll(() => {
    Object.defineProperty(Element.prototype, 'animate', {
      configurable: true,
      value: vi.fn(() => ({ cancel: vi.fn(), currentTime: 0, effect: {}, playState: 'finished' })),
    });
  });
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.getAll.mockResolvedValue([connection]);
    mocks.get.mockResolvedValue(connection);
    mocks.getWorkspaces.mockResolvedValue([{ id: 3, key: 'OPS', name: 'Operations' }]);
    mocks.update.mockResolvedValue(connection);
  });
  afterEach(cleanup);
  it('updates with the current revision and omits a blank token', async () => {
    render(NetBoxConnectionManager);
    await screen.findByText('Primary');
    await fireEvent.click(screen.getByRole('button', { name: 'common.edit' }));
    const dialog = screen.getByRole('dialog');
    await fireEvent.input(within(dialog).getByDisplayValue('Primary'), {
      target: { value: 'Renamed' },
    });
    await fireEvent.click(within(dialog).getByRole('button', { name: 'netbox.save' }));
    await waitFor(() =>
      expect(mocks.update).toHaveBeenCalledWith(
        'n1',
        expect.objectContaining({ revision: 7, name: 'Renamed' })
      )
    );
    expect(mocks.update.mock.calls[0][1]).not.toHaveProperty('api_token');
  });
  it('sends an explicit false workspace scope on create', async () => {
    mocks.getAll.mockResolvedValue([]);
    mocks.create.mockResolvedValue(connection);
    render(NetBoxConnectionManager);
    await screen.findByText('netbox.noConnections');
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.addConnection' }));
    const dialog = screen.getByRole('dialog');
    const inputs = within(dialog).getAllByRole('textbox');
    await fireEvent.input(inputs[0], { target: { value: 'Primary' } });
    await fireEvent.input(inputs[1], { target: { value: 'primary' } });
    await fireEvent.input(inputs[2], { target: { value: 'https://netbox.test' } });
    await fireEvent.input(within(dialog).getByLabelText(/^netbox\.apiToken/), {
      target: { value: 'nbt_key.token' },
    });
    await fireEvent.click(within(dialog).getByText('OPS - Operations'));
    await fireEvent.click(within(dialog).getByRole('button', { name: 'netbox.save' }));
    await waitFor(() =>
      expect(mocks.create).toHaveBeenCalledWith(
        expect.objectContaining({ applies_to_all_workspaces: false, workspace_ids: [3] })
      )
    );
  });

  it('submits only once when Enter repeats while creation is pending', async () => {
    mocks.getAll.mockResolvedValue([]);
    const creation = Promise.withResolvers();
    mocks.create.mockReturnValue(creation.promise);
    render(NetBoxConnectionManager);
    await screen.findByText('netbox.noConnections');
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.addConnection' }));
    const dialog = screen.getByRole('dialog');
    await fireEvent.input(within(dialog).getByLabelText(/^netbox\.name/), {
      target: { value: 'Primary' },
    });
    await fireEvent.input(within(dialog).getByLabelText(/^netbox\.slug/), {
      target: { value: 'primary' },
    });
    await fireEvent.input(within(dialog).getByLabelText(/^netbox\.baseUrl/), {
      target: { value: 'https://netbox.test' },
    });
    await fireEvent.input(within(dialog).getByLabelText(/^netbox\.apiToken/), {
      target: { value: 'nbt_key.token' },
    });
    await fireEvent.click(within(dialog).getByText('OPS - Operations'));
    const nameInput = within(dialog).getByLabelText(/^netbox\.name/);
    await fireEvent.keyDown(nameInput, { key: 'Enter' });
    await fireEvent.keyDown(nameInput, { key: 'Enter' });
    expect(mocks.create).toHaveBeenCalledTimes(1);
    creation.resolve(connection);
  });

  it('does not commit a late administration load after unmount', async () => {
    const late = Promise.withResolvers();
    mocks.getAll.mockReturnValue(late.promise);
    const view = render(NetBoxConnectionManager);
    view.unmount();
    late.resolve([connection]);
    await act(async () => {});
    expect(screen.queryByText('Primary')).not.toBeInTheDocument();
  });

  it('passes the abort signal in the workspace request-options position', async () => {
    render(NetBoxConnectionManager);
    await screen.findByText('Primary');
    expect(mocks.getWorkspaces).toHaveBeenCalledWith(
      {},
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    );
  });

  it('does not test a disabled connection', async () => {
    mocks.getAll.mockResolvedValue([{ ...connection, enabled: false }]);
    render(NetBoxConnectionManager);
    await screen.findByText('Primary');
    const button = screen.getByRole('button', { name: 'netbox.test' });
    expect(button).toBeDisabled();
    expect(screen.getByText('netbox.enableBeforeTesting')).toBeInTheDocument();
    await fireEvent.click(button);
    expect(mocks.test).not.toHaveBeenCalled();
  });

  it('blocks blind retry after 409 and reloads the fresh revision explicitly', async () => {
    const conflict = Object.assign(new Error('conflict'), { status: 409 });
    mocks.update
      .mockRejectedValueOnce(conflict)
      .mockResolvedValueOnce({ ...connection, revision: 8 });
    mocks.get.mockResolvedValue({ ...connection, revision: 8, name: 'Fresh' });
    render(NetBoxConnectionManager);
    await screen.findByText('Primary');
    await fireEvent.click(screen.getByRole('button', { name: 'common.edit' }));
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.save' }));
    await screen.findByText('netbox.editConflict');
    expect(screen.getByRole('button', { name: 'netbox.save' })).toBeDisabled();
    expect(mocks.update).toHaveBeenCalledTimes(1);
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.discardAndReload' }));
    await screen.findByDisplayValue('Fresh');
    await fireEvent.click(screen.getByRole('button', { name: 'aria.close' }));
    await fireEvent.click(screen.getByRole('button', { name: 'common.edit' }));
    expect(screen.getByDisplayValue('Fresh')).toBeInTheDocument();
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.save' }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(2));
    expect(mocks.update.mock.calls[1][1]).toEqual(expect.objectContaining({ revision: 8 }));
  });

  it('does not apply a late conflict reload to a newly opened dialog', async () => {
    const other = { ...connection, id: 'n2', slug: 'other', name: 'Other', revision: 3 };
    mocks.getAll.mockResolvedValue([connection, other]);
    mocks.update.mockRejectedValueOnce(Object.assign(new Error('conflict'), { status: 409 }));
    const lateReload = Promise.withResolvers();
    mocks.get.mockReturnValue(lateReload.promise);
    render(NetBoxConnectionManager);
    await screen.findByText('Primary');
    await fireEvent.click(screen.getAllByRole('button', { name: 'common.edit' })[0]);
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.save' }));
    await screen.findByText('netbox.editConflict');
    await fireEvent.click(screen.getByRole('button', { name: 'netbox.discardAndReload' }));
    await fireEvent.click(screen.getByRole('button', { name: 'aria.close' }));
    await fireEvent.click(screen.getAllByRole('button', { name: 'common.edit' })[1]);
    expect(screen.getByDisplayValue('Other')).toBeInTheDocument();
    lateReload.resolve({ ...connection, revision: 8, name: 'Fresh primary' });
    await act(async () => {});
    expect(screen.getByDisplayValue('Other')).toBeInTheDocument();
    expect(screen.queryByDisplayValue('Fresh primary')).not.toBeInTheDocument();
  });
});
