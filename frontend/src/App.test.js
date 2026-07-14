import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('./lib/router.js', async () => {
  const { writable } = await import('svelte/store');
  return {
    currentRoute: writable({ view: 'mobile-my-work', path: '/m', params: {} }),
    initRouter: vi.fn(),
    isMobileRoute: vi.fn((view) => String(view).startsWith('mobile-')),
    navigate: vi.fn(),
  };
});

vi.mock('./lib/stores', async () => {
  const { writable } = await import('svelte/store');
  const authStore = writable({
    isAuthenticated: false,
    loading: false,
    currentUser: null,
  });
  authStore.init = vi.fn();
  Object.defineProperty(authStore, 'currentUser', {
    get: () => null,
  });
  return { authStore };
});

vi.mock('./lib/stores/moduleSettings.js', () => ({
  moduleSettings: { load: vi.fn(), reload: vi.fn() },
}));

vi.mock('./lib/api.js', () => ({
  api: {
    setup: { getStatus: vi.fn() },
    themes: { getActive: vi.fn() },
  },
}));

vi.mock('./lib/stores/theme.svelte.js', () => ({
  themeStore: {
    resolvedTheme: 'light',
    init: vi.fn(),
    setActiveTheme: vi.fn(),
  },
}));

vi.mock('./lib/stores/i18n.svelte.js', () => ({
  i18n: {
    locale: 'en',
    direction: 'ltr',
    init: vi.fn().mockResolvedValue(undefined),
    setLocale: vi.fn(),
  },
  SUPPORTED_LOCALES: [{ code: 'en' }],
}));

vi.mock('./lib/mobile/MobileShell.svelte', async () => ({
  default: (await import('./test-fixtures/AppReadyStub.svelte')).default,
}));

vi.mock('./lib/dialogs/LoginDialog.svelte', async () => ({
  default: (await import('./test-fixtures/AppDialogStub.svelte')).default,
}));
vi.mock('./lib/pages/WelcomeAssistant.svelte', async () => ({
  default: (await import('./test-fixtures/AppDialogStub.svelte')).default,
}));
vi.mock('./lib/layout/Portal.svelte', async () => ({
  default: (await import('./test-fixtures/AppDialogStub.svelte')).default,
}));
vi.mock('./lib/features/forms/PublicFormPage.svelte', async () => ({
  default: (await import('./test-fixtures/AppDialogStub.svelte')).default,
}));
vi.mock('./lib/pages/SetPassword.svelte', async () => ({
  default: (await import('./test-fixtures/AppDialogStub.svelte')).default,
}));
vi.mock('./lib/pages/MainApp.svelte', async () => ({
  default: (await import('./test-fixtures/AppDialogStub.svelte')).default,
}));
vi.mock('./lib/pages/PublicBoard.svelte', async () => ({
  default: (await import('./test-fixtures/AppDialogStub.svelte')).default,
}));
vi.mock('./lib/features/pages/PagePrintView.svelte', async () => ({
  default: (await import('./test-fixtures/AppDialogStub.svelte')).default,
}));

import App from './App.svelte';
import { api } from './lib/api.js';
import { authStore } from './lib/stores';
import { i18n } from './lib/stores/i18n.svelte.js';

const authenticatedState = {
  isAuthenticated: true,
  loading: false,
  currentUser: { id: 1, language: 'en' },
};

describe('App startup recovery', () => {
  beforeEach(() => {
    authStore.set({ isAuthenticated: false, loading: false, currentUser: null });
    authStore.init.mockReset().mockImplementation(async () => {
      authStore.set(authenticatedState);
      return { status: 'authenticated' };
    });
    api.setup.getStatus.mockReset().mockResolvedValue({ setup_completed: true });
    api.themes.getActive.mockReset().mockResolvedValue({});
    i18n.init.mockReset().mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.useRealTimers();
    document.body.innerHTML = '';
  });

  it('renders the authenticated shell without waiting for theme loading', async () => {
    api.themes.getActive.mockReturnValue(new Promise(() => {}));

    render(App);

    expect(await screen.findByTestId('app-shell-ready')).toBeInTheDocument();
    expect(api.themes.getActive).toHaveBeenCalledWith({ timeout: 10_000 });
  });

  it('shows a connection error and retries bootstrap in place', async () => {
    api.setup.getStatus
      .mockRejectedValueOnce(Object.assign(new Error('offline'), { code: 'NETWORK_ERROR' }))
      .mockResolvedValueOnce({ setup_completed: true });

    render(App);

    expect(await screen.findByTestId('startup-error')).toHaveTextContent(
      'Windshift could not connect to the server.'
    );
    await fireEvent.click(screen.getByTestId('startup-retry'));

    await waitFor(() => expect(screen.getByTestId('app-shell-ready')).toBeInTheDocument());
    expect(api.setup.getStatus).toHaveBeenCalledTimes(2);
  });

  it('times out a stalled locale chunk instead of loading forever', async () => {
    vi.useFakeTimers();
    i18n.init.mockReturnValue(new Promise(() => {}));

    render(App);
    await vi.advanceTimersByTimeAsync(10_000);

    expect(screen.getByTestId('startup-error')).toHaveTextContent(
      'The server took too long to respond.'
    );
    expect(api.setup.getStatus).not.toHaveBeenCalled();
  });
});
