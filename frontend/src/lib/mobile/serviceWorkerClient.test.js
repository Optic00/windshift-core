import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  registerMobileServiceWorker,
  resetServiceWorkerRegistrationForTests,
} from './serviceWorkerClient.js';

describe('registerMobileServiceWorker', () => {
  beforeEach(() => {
    resetServiceWorkerRegistrationForTests();
    document.head.innerHTML = '<base href="/windshift/">';
  });

  afterEach(() => {
    document.head.innerHTML = '';
    delete navigator.serviceWorker;
  });

  it('registers once using the context-path script and scope', async () => {
    const registration = { scope: '/windshift/' };
    const register = vi.fn().mockResolvedValue(registration);
    Object.defineProperty(navigator, 'serviceWorker', {
      value: { register },
      configurable: true,
    });

    const first = registerMobileServiceWorker();
    const second = registerMobileServiceWorker();

    await expect(first).resolves.toBe(registration);
    await expect(second).resolves.toBe(registration);
    expect(register).toHaveBeenCalledTimes(1);
    expect(register).toHaveBeenCalledWith('/windshift/service-worker.js', {
      scope: '/windshift/',
    });
  });

  it('allows a later retry after registration fails', async () => {
    const register = vi
      .fn()
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce({ scope: '/windshift/' });
    Object.defineProperty(navigator, 'serviceWorker', {
      value: { register },
      configurable: true,
    });

    await expect(registerMobileServiceWorker()).resolves.toBeNull();
    await expect(registerMobileServiceWorker()).resolves.toEqual({ scope: '/windshift/' });
    expect(register).toHaveBeenCalledTimes(2);
  });
});
