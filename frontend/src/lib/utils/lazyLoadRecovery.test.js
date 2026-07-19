import { describe, expect, it, vi } from 'vitest';
import { hasSessionExpired, LAZY_LOAD_SESSION_CHECK_TIMEOUT_MS } from './lazyLoadRecovery.js';

describe('hasSessionExpired', () => {
  it('identifies an unauthenticated session response', async () => {
    const checkSession = vi
      .fn()
      .mockRejectedValue(Object.assign(new Error('expired'), { status: 401 }));

    await expect(hasSessionExpired(checkSession)).resolves.toBe(true);
    expect(checkSession).toHaveBeenCalledWith({ timeout: LAZY_LOAD_SESSION_CHECK_TIMEOUT_MS });
  });

  it('does not treat connectivity failures as an expired session', async () => {
    const checkSession = vi
      .fn()
      .mockRejectedValue(Object.assign(new Error('offline'), { status: 0 }));

    await expect(hasSessionExpired(checkSession)).resolves.toBe(false);
  });

  it('keeps a valid session authenticated', async () => {
    await expect(hasSessionExpired(vi.fn().mockResolvedValue({ user: {} }))).resolves.toBe(false);
  });
});
