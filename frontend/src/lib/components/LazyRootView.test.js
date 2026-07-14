import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';

vi.mock('../stores/i18n.svelte.js', () => ({ t: (key) => key }));

import LazyRootView from './LazyRootView.svelte';
import Spinner from './Spinner.svelte';

describe('LazyRootView', () => {
  it('shows a loading state while its route chunk is pending', () => {
    const loader = vi.fn(() => new Promise(() => {}));

    render(LazyRootView, { loader, label: 'workspace' });

    expect(screen.getByTestId('root-view-loading')).toHaveTextContent('Loading workspace');
    expect(loader).toHaveBeenCalledTimes(1);
  });

  it('retries a failed route chunk without reloading the application', async () => {
    const loader = vi
      .fn()
      .mockRejectedValueOnce(new Error('chunk unavailable'))
      .mockResolvedValueOnce({ default: Spinner });

    render(LazyRootView, { loader, label: 'workspace' });
    expect(await screen.findByTestId('root-view-error')).toHaveTextContent(
      'Unable to load workspace'
    );

    await fireEvent.click(screen.getByRole('button', { name: 'Try again' }));

    await waitFor(() => expect(loader).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByTestId('root-view-error')).not.toBeInTheDocument());
  });
});
