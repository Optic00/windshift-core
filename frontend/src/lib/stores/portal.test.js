import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  auth: {
    internal: false,
    portalCustomer: false,
    portalInternal: false,
  },
  assetReports: {
    getForChannel: vi.fn(),
    getForPortal: vi.fn(),
  },
  assetSets: {
    getAll: vi.fn(),
  },
  portal: {
    get: vi.fn(),
  },
  requestTypes: {
    getForPortal: vi.fn(),
  },
}));

vi.mock('../api.js', () => ({
  api: {
    assetReports: mocks.assetReports,
    assetSets: mocks.assetSets,
    portal: mocks.portal,
    requestTypes: mocks.requestTypes,
  },
}));

vi.mock('../router.js', () => ({
  navigate: vi.fn(),
}));

vi.mock('../stores', () => ({
  authStore: {
    get isAuthenticated() {
      return mocks.auth.internal;
    },
  },
}));

vi.mock('./portalAuth.svelte.js', () => ({
  portalAuthStore: {
    get isAuthenticated() {
      return mocks.auth.portalCustomer || mocks.auth.portalInternal;
    },
    get isInternal() {
      return mocks.auth.portalInternal;
    },
  },
}));

vi.mock('./toasts.svelte.js', () => ({
  errorToast: vi.fn(),
}));

const { portalStore } = await import('./portal.svelte.js');

const publicReports = [{ id: 1, name: 'Audience report', is_active: true }];
const managerReports = [
  ...publicReports,
  { id: 2, name: 'Inactive definition', is_active: false, cql_query: 'secret = true' },
];

function deferred() {
  let resolve;
  const promise = new Promise((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe('portal asset-report loading', () => {
  beforeEach(() => {
    portalStore.reset();
    vi.clearAllMocks();
    mocks.auth.internal = false;
    mocks.auth.portalCustomer = false;
    mocks.auth.portalInternal = false;
    mocks.portal.get.mockResolvedValue({
      channel_id: 7,
      slug: 'support',
      title: 'Support',
      sections: [],
      workspace_ids: [],
    });
    mocks.requestTypes.getForPortal.mockResolvedValue([]);
    mocks.assetReports.getForPortal.mockResolvedValue(publicReports);
    mocks.assetReports.getForChannel.mockResolvedValue(managerReports);
    mocks.assetSets.getAll.mockResolvedValue([{ id: 11 }]);
  });

  it.each([
    ['guest', {}],
    ['portal customer', { portalCustomer: true }],
    ['internal non-manager', { internal: true }],
    ['channel manager', { internal: true }],
  ])('loads the %s audience through the public portal endpoint', async (_viewer, auth) => {
    Object.assign(mocks.auth, auth);

    await portalStore.loadPortal('support');

    expect(mocks.assetReports.getForPortal).toHaveBeenCalledWith('support');
    expect(mocks.assetReports.getForChannel).not.toHaveBeenCalled();
    expect(mocks.assetSets.getAll).not.toHaveBeenCalled();
    expect(portalStore.assetReports).toEqual(publicReports);
  });

  it('uses manager definitions only while customization is open', async () => {
    mocks.auth.internal = true;
    await portalStore.loadPortal('support');

    portalStore.showCustomizePanel = true;
    await vi.waitFor(() => expect(portalStore.assetReports).toEqual(managerReports));
    expect(mocks.assetReports.getForChannel).toHaveBeenCalledWith(7);

    mocks.assetReports.getForPortal.mockClear();
    portalStore.showCustomizePanel = false;
    await vi.waitFor(() => expect(portalStore.assetReports).toEqual(publicReports));
    expect(mocks.assetReports.getForPortal).toHaveBeenCalledWith('support');
  });

  it('does not expose audience data as editable definitions when manager access is denied', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    mocks.auth.internal = true;
    mocks.assetReports.getForChannel.mockRejectedValue(
      Object.assign(new Error('Not found'), { status: 404 })
    );
    await portalStore.loadPortal('support');

    portalStore.showCustomizePanel = true;

    await vi.waitFor(() => expect(portalStore.loadingAssetReports).toBe(false));
    expect(mocks.assetReports.getForChannel).toHaveBeenCalledWith(7);
    expect(portalStore.assetReports).toEqual([]);
    errorSpy.mockRestore();
  });

  it('discards a delayed manager response after returning to audience mode', async () => {
    mocks.auth.internal = true;
    await portalStore.loadPortal('support');
    const managerLoad = deferred();
    mocks.assetReports.getForChannel.mockReturnValue(managerLoad.promise);

    portalStore.showCustomizePanel = true;
    await vi.waitFor(() => expect(mocks.assetReports.getForChannel).toHaveBeenCalledWith(7));
    portalStore.showCustomizePanel = false;
    await vi.waitFor(() => expect(portalStore.assetReports).toEqual(publicReports));

    managerLoad.resolve(managerReports);
    await managerLoad.promise;
    await Promise.resolve();

    expect(portalStore.assetReports).toEqual(publicReports);
  });
});
