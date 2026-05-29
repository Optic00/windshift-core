<script>
  import { AlertTriangle, X } from '@lucide/svelte';
  import Button from '../components/Button.svelte';
  import Input from '../components/Input.svelte';
  import ModalBackdrop from '../components/ModalBackdrop.svelte';
  import { t } from '../stores/i18n.svelte.js';
  import { getShortcut, matchesShortcut } from '../utils/keyboardShortcuts.js';

  // Same shape as ConfirmDialog.svelte, plus a required reason input.
  // The Confirm button stays disabled until the reason is non-empty.
  // onconfirm receives the trimmed reason string so callers can audit-log it.
  let {
    show = $bindable(false),
    title = null,
    message = null,
    reasonLabel = null,
    reasonPlaceholder = null,
    confirmText = null,
    cancelText = null,
    variant = 'warning', // 'danger', 'warning', 'info'
    icon: Icon = AlertTriangle,
    onconfirm = null,
    oncancel = null,
  } = $props();

  const submitShortcut = getShortcut('modal', 'submit');

  const resolvedTitle = $derived(title ?? t('common.areYouSure'));
  const resolvedMessage = $derived(message ?? t('common.confirmAction'));
  const resolvedReasonLabel = $derived(reasonLabel ?? 'Reason (audit-logged)');
  const resolvedReasonPlaceholder = $derived(reasonPlaceholder ?? 'Why are you making this change?');
  const resolvedConfirmText = $derived(confirmText ?? t('common.confirm'));
  const resolvedCancelText = $derived(cancelText ?? t('common.cancel'));

  let reason = $state('');
  let inputEl = $state(null);

  // Reset the reason whenever the dialog opens; focus the input after the
  // backdrop has rendered.
  $effect(() => {
    if (show) {
      reason = '';
      queueMicrotask(() => inputEl?.focus());
    }
  });

  let canConfirm = $derived(reason.trim().length > 0);

  function doConfirm() {
    if (!canConfirm) return;
    const trimmed = reason.trim();
    onconfirm?.(trimmed);
    show = false;
  }

  function cancel() {
    oncancel?.();
    show = false;
  }

  function handleKeydown(event) {
    if (!show) return;
    if (matchesShortcut(event, submitShortcut)) {
      event.preventDefault();
      doConfirm();
      return;
    }
    if (event.key === 'Enter' && !event.ctrlKey && !event.metaKey && !event.shiftKey) {
      const activeElement = document.activeElement;
      const isOnCancelButton = activeElement?.textContent?.trim() === resolvedCancelText;
      if (!isOnCancelButton) {
        event.preventDefault();
        doConfirm();
      }
    }
  }

  function getVariantStyles(v) {
    switch (v) {
      case 'danger':
        return { iconColor: 'var(--ds-icon-danger)', buttonVariant: 'danger' };
      case 'warning':
        return { iconColor: 'var(--ds-icon-warning)', buttonVariant: 'primary' };
      case 'info':
        return { iconColor: 'var(--ds-icon-info)', buttonVariant: 'primary' };
      default:
        return { iconColor: 'var(--ds-icon)', buttonVariant: 'primary' };
    }
  }

  let styles = $derived(getVariantStyles(variant));
</script>

<svelte:window onkeydown={handleKeydown} />

<ModalBackdrop bind:show onclose={cancel} ariaLabelledBy="reason-dialog-title" zIndex={70}>
  <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
  <div
    role="presentation"
    class="bg-white rounded shadow-xl max-w-md w-full transform transition-all"
    style="background-color: var(--ds-surface-raised);"
    onclick={(e) => e.stopPropagation()}
  >
    <!-- Header -->
    <div class="px-6 py-4 border-b" style="border-color: var(--ds-border);">
      <div class="flex items-center gap-3">
        {#if Icon}
          <div class="flex-shrink-0">
            <Icon class="w-6 h-6" style="color: {styles.iconColor};" />
          </div>
        {/if}
        <h3
          id="reason-dialog-title"
          class="text-lg font-medium flex-1"
          style="color: var(--ds-text);"
        >
          {resolvedTitle}
        </h3>
        <Button variant="ghost" icon={X} onclick={cancel} title={t('common.close')} />
      </div>
    </div>

    <!-- Body -->
    <div class="px-6 py-4 space-y-4">
      <p class="text-sm leading-relaxed" style="color: var(--ds-text-subtle);">
        {resolvedMessage}
      </p>
      <div>
        <label for="confirm-reason-input" class="block text-sm font-medium mb-2" style="color: var(--ds-text);">
          {resolvedReasonLabel}
        </label>
        <Input
          id="confirm-reason-input"
          bind:value={reason}
          bind:inputRef={inputEl}
          placeholder={resolvedReasonPlaceholder}
          required
        />
      </div>
    </div>

    <!-- Footer -->
    <div class="px-6 py-4 border-t flex justify-end gap-3" style="border-color: var(--ds-border);">
      <Button variant="default" onclick={cancel} size="small" keyboardHint="Esc" dataTestid="reason-dialog-cancel">
        {resolvedCancelText}
      </Button>
      <Button
        variant={styles.buttonVariant}
        onclick={doConfirm}
        size="small"
        keyboardHint="↵"
        disabled={!canConfirm}
        dataTestid="reason-dialog-confirm"
      >
        {resolvedConfirmText}
      </Button>
    </div>
  </div>
</ModalBackdrop>
