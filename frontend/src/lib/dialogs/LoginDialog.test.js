import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  clearError: vi.fn(),
  getPublicStatus: vi.fn(),
  startLogin: vi.fn(),
}));

vi.mock('../stores', async () => {
  const { writable } = await import('svelte/store');
  const authStore = Object.assign(writable({ loading: false, error: null }), {
    clearError: mocks.clearError,
    login: vi.fn(),
  });
  const ssoStore = Object.assign(
    writable({
      enabled: true,
      providerName: 'Acme SSO',
      providers: [{ slug: 'acme', name: 'Acme SSO', provider_type: 'oidc' }],
      statusLoading: false,
    }),
    {
      initStatus: vi.fn().mockResolvedValue(undefined),
      checkForError: vi.fn().mockReturnValue(null),
      startLogin: mocks.startLogin,
    }
  );
  return { authStore, ssoStore };
});

vi.mock('../api.js', () => ({ api: {} }));
vi.mock('../api/admin.js', () => ({
  authPolicy: { getPublicStatus: mocks.getPublicStatus },
}));
vi.mock('../router.js', () => ({ navigate: vi.fn() }));
vi.mock('../utils/webauthn-utils.js', () => ({
  isWebAuthnSupported: vi.fn().mockReturnValue(false),
}));
vi.mock('../utils/loginUtils.js', () => ({
  deriveFidoError: vi.fn(),
  evaluateFidoAvailability: vi.fn().mockResolvedValue({ available: false, showOption: false }),
  getBaseLoginState: vi.fn().mockReturnValue({
    emailOrUsername: '',
    password: '',
    rememberMe: false,
    showPassword: false,
    validationError: '',
    fidoAvailable: false,
    tryingFido: false,
    showFidoOption: false,
  }),
  performFidoLogin: vi.fn(),
}));
vi.mock('../stores/i18n.svelte.js', () => ({
  t: (key, params = {}) => {
    if (key === 'auth.staySignedIn') return 'Keep me signed in to Windshift for 30 days';
    if (key === 'auth.continueWith') return `Continue with ${params.provider}`;
    return key;
  },
}));

import LoginDialog from './LoginDialog.svelte';

beforeEach(() => {
  mocks.clearError.mockClear();
  mocks.startLogin.mockClear();
  mocks.getPublicStatus.mockReset().mockResolvedValue({
    hide_password_form: true,
    sso_enabled: true,
    passkey_required: false,
  });
});

describe('LoginDialog SSO remember-me', () => {
  test('shows the 30-day choice before SSO and forwards it in SSO-only mode', async () => {
    render(LoginDialog, { props: { isOpen: true } });

    const rememberMe = await screen.findByRole('checkbox', {
      name: 'Keep me signed in to Windshift for 30 days',
    });
    const ssoButton = screen.getByRole('button', { name: 'Continue with Acme SSO' });

    expect(
      rememberMe.compareDocumentPosition(ssoButton) & Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy();
    await waitFor(() => expect(screen.getByText('Password login is disabled')).toBeInTheDocument());

    await fireEvent.click(rememberMe);
    await fireEvent.click(ssoButton);

    expect(mocks.startLogin).toHaveBeenCalledWith(true);
  });
});
