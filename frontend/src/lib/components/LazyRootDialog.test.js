import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import LazyRootDialog from './LazyRootDialog.svelte';
import DialogContent from './test-fixtures/LazyRootDialogContent.svelte';

describe('LazyRootDialog', () => {
  it('forwards callback props and the bindable open state', async () => {
    const onaction = vi.fn();
    const loader = vi.fn().mockResolvedValue({ default: DialogContent });

    render(LazyRootDialog, {
      loader,
      label: 'sign in',
      isOpen: true,
      componentProps: { onaction },
    });

    const content = await screen.findByTestId('lazy-dialog-content');
    expect(content).toHaveTextContent('Open');
    await fireEvent.click(content);

    expect(content).toHaveTextContent('Closed');
    expect(onaction).toHaveBeenCalledTimes(1);
  });
});
