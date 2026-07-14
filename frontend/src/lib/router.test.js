import { beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { initRouter } from './router.js';

beforeAll(() => {
  initRouter();
});

beforeEach(() => {
  window.history.replaceState({}, '', '/');
  document.body.innerHTML = '';
});

function clickLink(href) {
  const anchor = document.createElement('a');
  anchor.href = href;
  document.body.appendChild(anchor);
  const event = new MouseEvent('click', {
    bubbles: true,
    cancelable: true,
    button: 0,
  });
  let wasPrevented = false;
  document.addEventListener(
    'click',
    (clickEvent) => {
      // The router listener was registered first, so this records its decision
      // before suppressing jsdom's unimplemented native document navigation.
      wasPrevented = clickEvent.defaultPrevented;
      clickEvent.preventDefault();
    },
    { once: true }
  );
  anchor.dispatchEvent(event);
  anchor.remove();
  return wasPrevented;
}

function originPrefixedExternalURL() {
  const { protocol, hostname, port, origin } = window.location;
  if (port) return `${protocol}//${hostname}:${port}@evil.test/external`;
  return `${origin}.evil.test/external`;
}

describe('router link interception', () => {
  it('does not intercept an external URL whose text starts with the app origin', () => {
    const externalURL = originPrefixedExternalURL();
    expect(new URL(externalURL).origin).not.toBe(window.location.origin);

    const wasPrevented = clickLink(externalURL);

    expect(wasPrevented).toBe(false);
    expect(window.location.pathname).toBe('/');
  });

  it('leaves same-page fragment links to native browser navigation', () => {
    window.history.replaceState({}, '', '/api-docs');

    const wasPrevented = clickLink('#operation-list');

    expect(wasPrevented).toBe(false);
  });

  it('preserves the fragment when routing to another app page', () => {
    const wasPrevented = clickLink('/api-docs#operation-list');

    expect(wasPrevented).toBe(true);
    expect(window.location.pathname).toBe('/api-docs');
    expect(window.location.hash).toBe('#operation-list');
  });
});
