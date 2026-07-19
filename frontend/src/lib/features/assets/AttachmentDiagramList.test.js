import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, test, vi } from 'vitest';

vi.mock('../../stores/i18n.svelte.js', () => ({
  t: (key) => key,
}));

vi.mock('../../stores/toasts.svelte.js', () => ({
  errorToast: vi.fn(),
}));

import AttachmentDiagramList from './AttachmentDiagramList.svelte';

describe('AttachmentDiagramList deferred diagrams', () => {
  test('offers to load diagrams only until their deferred request completes', async () => {
    const onloadDiagrams = vi.fn();
    const view = render(AttachmentDiagramList, { onloadDiagrams });

    await fireEvent.click(screen.getByTestId('load-item-diagrams'));
    expect(onloadDiagrams).toHaveBeenCalledOnce();

    await view.rerender({ onloadDiagrams, diagramsLoaded: true });
    expect(screen.queryByTestId('load-item-diagrams')).not.toBeInTheDocument();
  });
});
