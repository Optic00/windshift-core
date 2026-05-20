import { navigate } from '../router.js';
import { addToast, removeToast } from '../stores/toasts.svelte.js';
import { isTypingInField } from './keyboardShortcuts.js';

const OPEN_SHORTCUT = 'o';
const DEFAULT_DURATION = 8000;

function getItemKey(item) {
  if (!item) return 'Item';
  const key = item.workspace_key || item.workspaceKey || 'WORK';
  const number = item.workspace_item_number || item.workspaceItemNumber || item.id;
  return `${key}-${number}`;
}

function getItemUrl(item) {
  return `/workspaces/${item.workspace_id}/items/${item.id}`;
}

/**
 * Show a success toast for a newly-created work item. The toast is clickable
 * and registers a short-lived "O" shortcut to open the item detail page.
 *
 * @param {object} item
 * @param {{ messagePrefix?: string, duration?: number }} [options]
 */
export function showCreatedItemToast(item, options = {}) {
  if (!item?.id) return null;

  const itemKey = getItemKey(item);
  const duration = options.duration ?? DEFAULT_DURATION;
  const openItem = () => navigate(getItemUrl(item));

  let toastId = null;
  let cleanupTimer = null;

  const cleanup = () => {
    document.removeEventListener('keydown', handleKeydown);
    if (cleanupTimer) {
      clearTimeout(cleanupTimer);
      cleanupTimer = null;
    }
  };

  const closeAndOpen = () => {
    // removeToast fires onDismiss which runs cleanup — no need to call it here.
    if (toastId !== null) removeToast(toastId);
    openItem();
  };

  function handleKeydown(event) {
    if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey)
      return;
    if (isTypingInField(event)) return;
    if (event.key?.toLowerCase() !== OPEN_SHORTCUT) return;

    event.preventDefault();
    closeAndOpen();
  }

  document.addEventListener('keydown', handleKeydown);
  cleanupTimer = setTimeout(cleanup, duration + 250);

  toastId = addToast({
    title: `${itemKey} created`,
    message: `${options.messagePrefix ? `${options.messagePrefix} ` : ''}${item.title || 'Untitled'}`,
    variant: 'success',
    duration,
    clickable: true,
    actionLabel: 'Open item',
    keyboardHint: 'O',
    onClick: closeAndOpen,
    onDismiss: cleanup,
  });

  return toastId;
}
