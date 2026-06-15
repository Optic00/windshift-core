import { render } from '@testing-library/svelte';
import { afterEach, beforeAll, describe, expect, test, vi } from 'vitest';

// jsdom does not implement the Web Animations API. Svelte 5 transitions
// call element.animate during outro. Stub it so transitions resolve.
beforeAll(() => {
  if (!Element.prototype.animate) {
    Element.prototype.animate = () => ({
      finished: Promise.resolve(),
      cancel: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      play: () => {},
      pause: () => {},
    });
  }
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = () => {};
  }
  if (!globalThis.ResizeObserver) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  }
});

// MentionPicker fetches users from the API on mount. Provide a fixed roster.
vi.mock('../api.js', () => ({
  api: {
    getUsers: vi.fn(async () => [
      { username: 'alice', first_name: 'Alice', last_name: 'Johnson' },
      { username: 'bob', first_name: 'Bob', last_name: 'Smith' },
    ]),
  },
}));

// i18n.t — return the key verbatim so assertions don't depend on locale data.
vi.mock('../stores/i18n.svelte.js', () => ({
  t: vi.fn((key) => key),
}));

// Stub the child presentational components (Svelte 5 component-mock style).
vi.mock('../components/Avatar.svelte', () => ({
  default: function MockAvatar() {},
}));
vi.mock('../components/Text.svelte', () => ({
  default: function MockText() {},
}));

// Capture the document-level keydown handler that runed's useEventListener
// registers, so the test can dispatch real KeyboardEvents at it. The picker
// listens in the capture phase — mirror that so the test path matches prod.
let keydownHandler = null;
let registeredCapture = null;
vi.mock('runed', () => ({
  useEventListener: (_target, _event, handler, options) => {
    // target is a thunk returning document; we only care about keydown.
    keydownHandler = handler;
    registeredCapture = options?.capture ?? false;
    document.addEventListener('keydown', handler, options);
  },
}));

import MentionPicker from './MentionPicker.svelte';

afterEach(() => {
  if (keydownHandler) {
    document.removeEventListener('keydown', keydownHandler, { capture: registeredCapture });
  }
  keydownHandler = null;
  registeredCapture = null;
  document.body.innerHTML = '';
});

function pressKey(key) {
  const event = new KeyboardEvent('keydown', {
    key,
    bubbles: true,
    cancelable: true,
  });
  // Spy on the instance methods rather than the listener wrapper so we can
  // assert the picker consumed the key.
  const preventDefault = vi.spyOn(event, 'preventDefault');
  const stopPropagation = vi.spyOn(event, 'stopPropagation');
  document.dispatchEvent(event);
  return { event, preventDefault, stopPropagation };
}

describe('MentionPicker — Enter while open must not break the mention (WI-200)', () => {
  test('Enter with results selects the highlighted user and consumes the key', async () => {
    const onSelect = vi.fn();
    render(MentionPicker, { props: { open: true, onSelect } });

    // onMount kicks off getUsers(); wait for the list to populate.
    await vi.waitFor(() => {
      expect(document.querySelector('[role="option"]')).toBeTruthy();
    });

    const { preventDefault, stopPropagation } = pressKey('Enter');

    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith(
      expect.objectContaining({ username: 'alice' })
    );
    expect(preventDefault).toHaveBeenCalled();
    expect(stopPropagation).toHaveBeenCalled();
  });

  test('Enter with zero matching results consumes the key but selects nothing', async () => {
    // This is the WI-200 regression: previously the handler bailed early on
    // an empty filtered list, leaving Enter to fall through to ProseMirror,
    // which inserted a newline at the cursor and split the in-progress
    // @mention into a broken chip.
    const onSelect = vi.fn();
    render(MentionPicker, { props: { open: true, query: 'zzznomatch', onSelect } });

    await vi.waitFor(() => {
      expect(document.querySelector('[role="option"]')).toBeNull();
    });

    const { preventDefault, stopPropagation } = pressKey('Enter');

    expect(onSelect).not.toHaveBeenCalled();
    // The key must still be consumed so the editor can't insert a newline.
    expect(preventDefault).toHaveBeenCalled();
    expect(stopPropagation).toHaveBeenCalled();
  });

  test('Enter is a no-op (not consumed) when the picker is closed', async () => {
    const onSelect = vi.fn();
    render(MentionPicker, { props: { open: false, onSelect } });

    const { preventDefault } = pressKey('Enter');

    expect(onSelect).not.toHaveBeenCalled();
    expect(preventDefault).not.toHaveBeenCalled();
  });

  test('Escape cancels and consumes the key even with zero results', async () => {
    const onCancel = vi.fn();
    render(MentionPicker, { props: { open: true, query: 'zzznomatch', onCancel } });

    await vi.waitFor(() => {
      expect(document.querySelector('[role="option"]')).toBeNull();
    });

    const { preventDefault, stopPropagation } = pressKey('Escape');

    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(preventDefault).toHaveBeenCalled();
    expect(stopPropagation).toHaveBeenCalled();
  });

  test('ArrowDown navigates the list and consumes the key', async () => {
    const onSelect = vi.fn();
    render(MentionPicker, { props: { open: true, onSelect } });

    await vi.waitFor(() => {
      expect(document.querySelectorAll('[role="option"]').length).toBe(2);
    });

    const { preventDefault } = pressKey('ArrowDown');
    pressKey('Enter');

    expect(preventDefault).toHaveBeenCalled();
    expect(onSelect).toHaveBeenCalledWith(
      expect.objectContaining({ username: 'bob' })
    );
  });
});
