import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeAll, describe, expect, test, vi } from 'vitest';

// jsdom does not implement the Web Animations API. Svelte 5 transitions
// call element.animate during outro. Stub it with a Promise-shaped result
// so transitions resolve immediately and don't crash the test runner.
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
  // jsdom also lacks scrollIntoView, which the picker uses to keep the
  // highlighted row visible.
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = () => {};
  }
  // Melt's floating menu positioning observes the reference element.
  if (!globalThis.ResizeObserver) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  }
});

import BasePicker from './BasePicker.svelte';

afterEach(() => {
  // The dropdown menu is portaled to document.body and outros via a fly
  // transition — stragglers can outlive the component between renders.
  // Wipe the body so each test starts clean.
  document.body.innerHTML = '';
});

const items = [
  { id: 1, name: 'Apple' },
  { id: 2, name: 'Apricot' },
  { id: 3, name: 'Banana' },
];

async function openAndType(query) {
  const input = screen.getByRole('combobox');
  await fireEvent.click(input);
  await fireEvent.input(input, { target: { value: query } });
  return input;
}

describe('BasePicker — Enter selects the highlighted option vs create (WI-343)', () => {
  test('single: Enter on a partial query selects the highlighted match, not create', async () => {
    const onSelect = vi.fn();
    const onCreate = vi.fn();
    render(BasePicker, {
      props: { items, allowCreate: true, onCreate, onSelect },
    });

    const input = await openAndType('Ap');
    // Wait for the filtered dropdown to render (Apple highlighted first).
    await waitFor(() => {
      expect(document.querySelector('[data-option-value="1"]')).toBeInTheDocument();
    });

    await fireEvent.keyDown(input, { key: 'Enter' });

    expect(onCreate).not.toHaveBeenCalled();
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith(items[0]);
  });

  test('single: arrow-key highlight + Enter selects that option, not create', async () => {
    const onSelect = vi.fn();
    const onCreate = vi.fn();
    render(BasePicker, {
      props: { items, allowCreate: true, onCreate, onSelect },
    });

    const input = await openAndType('Ap');
    await waitFor(() => {
      expect(document.querySelector('[data-option-value="2"]')).toBeInTheDocument();
    });

    await fireEvent.keyDown(input, { key: 'ArrowDown' });
    await fireEvent.keyDown(input, { key: 'Enter' });

    expect(onCreate).not.toHaveBeenCalled();
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith(items[1]);
  });

  test('single: Enter with zero matches calls onCreate with the query', async () => {
    const onSelect = vi.fn();
    const onCreate = vi.fn();
    render(BasePicker, {
      props: { items, allowCreate: true, onCreate, onSelect },
    });

    // A non-matching query leaves zero selectable options (the outgoing
    // menu DOM may linger mid-transition, but the reactive option list —
    // which the keyboard handler consults — is empty).
    const input = await openAndType('Cherry');
    await fireEvent.keyDown(input, { key: 'Enter' });

    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1));
    expect(onCreate).toHaveBeenCalledWith('Cherry');
    expect(onSelect).not.toHaveBeenCalled();
  });

  test('single: Enter with zero matches but no create support is a no-op', async () => {
    const onSelect = vi.fn();
    render(BasePicker, {
      props: { items, onSelect },
    });

    const input = await openAndType('Cherry');
    await fireEvent.keyDown(input, { key: 'Enter' });

    expect(onSelect).not.toHaveBeenCalled();
  });

  test('multiple: Enter on a partial query toggles the highlighted match, not create', async () => {
    const onChange = vi.fn();
    const onCreate = vi.fn();
    render(BasePicker, {
      props: { items, multiple: true, value: [], allowCreate: true, onCreate, onChange },
    });

    const input = await openAndType('Ap');
    await waitFor(() => {
      expect(document.querySelector('[data-option-value="1"]')).toBeInTheDocument();
    });

    await fireEvent.keyDown(input, { key: 'Enter' });

    expect(onCreate).not.toHaveBeenCalled();
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith([1]);
  });

  test('multiple: Enter with zero matches calls onCreate with the query', async () => {
    const onChange = vi.fn();
    const onCreate = vi.fn();
    render(BasePicker, {
      props: { items, multiple: true, value: [], allowCreate: true, onCreate, onChange },
    });

    const input = await openAndType('Cherry');
    await fireEvent.keyDown(input, { key: 'Enter' });

    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1));
    expect(onCreate).toHaveBeenCalledWith('Cherry');
    expect(onChange).not.toHaveBeenCalled();
  });

  test('create still refuses an exact label match', async () => {
    const onSelect = vi.fn();
    const onCreate = vi.fn();
    render(BasePicker, {
      props: { items, allowCreate: true, onCreate, onSelect },
    });

    const input = await openAndType('Apple');
    await waitFor(() => {
      expect(document.querySelector('[data-option-value="1"]')).toBeInTheDocument();
    });

    await fireEvent.keyDown(input, { key: 'Enter' });

    expect(onCreate).not.toHaveBeenCalled();
    expect(onSelect).toHaveBeenCalledWith(items[0]);
  });
});
