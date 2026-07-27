<script>
  import Modal from '../../dialogs/Modal.svelte';
  import ModalHeader from '../../dialogs/ModalHeader.svelte';
  import Button from '../../components/Button.svelte';
  import FormRenderer from '../forms/FormRenderer.svelte';

  let {
    isOpen = $bindable(false),
    form = null,
    fields = [],
    customFieldDefinitions = [],
    formConfig = null,
    brandColor = '#14b8a6',
    onClose = () => {},
  } = $props();

  function close() {
    isOpen = false;
    onClose();
  }
</script>

<Modal bind:isOpen maxWidth="max-w-3xl" maxHeight="calc(100vh - 4rem)" onclose={close}>
  {#snippet children()}
    <ModalHeader title={`Preview: ${form?.name || 'Form'}`} onClose={close} />
    <div class="overflow-y-auto p-6" data-testid="form-preview-modal">
      {#if form}
        <FormRenderer
          formSlug="preview"
          formId={form.id}
          {formConfig}
          initialDetail={{
            form_id: form.id,
            fields,
            custom_field_definitions: customFieldDefinitions,
          }}
          {brandColor}
          preview
        />
      {/if}
    </div>
    <div class="flex justify-end border-t px-6 py-4" style="border-color: var(--ds-border);">
      <Button variant="default" onclick={close}>Close preview</Button>
    </div>
  {/snippet}
</Modal>
