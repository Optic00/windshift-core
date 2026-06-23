<script>
  // Workspace work item templates (WI-438). Reusable description bodies that
  // pre-fill a new item's description at creation time. A template is either
  // "selectable" (offered in the create-modal picker for the chosen item type
  // plus untyped globals) or "mandatory" (bound to exactly one item type and
  // auto-applied when the description is left empty; the picker is suppressed).

  import { onMount } from 'svelte';
  import { FileStack, Loader2, Pencil, Plus, Trash2 } from '@lucide/svelte';
  import { api } from '../api.js';
  import Panel from '../components/Panel.svelte';
  import Button from '../components/Button.svelte';
  import Checkbox from '../components/Checkbox.svelte';
  import Input from '../components/Input.svelte';
  import Label from '../components/Label.svelte';
  import Select from '../components/Select.svelte';
  import BasePicker from '../pickers/BasePicker.svelte';
  import MilkdownEditor from '../editors/LazyMilkdownEditor.svelte';
  import SectionHeader from '../layout/SectionHeader.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Modal from '../dialogs/Modal.svelte';
  import ModalHeader from '../dialogs/ModalHeader.svelte';
  import DialogFooter from '../dialogs/DialogFooter.svelte';
  import ConfirmDialog from '../dialogs/ConfirmDialog.svelte';
  import { errorToast, successToast } from '../stores/toasts.svelte.js';
  import { toHotkeyString } from '../utils/keyboardShortcuts.js';

  let { workspaceId } = $props();

  const MODE_OPTIONS = [
    { value: 'selectable', label: 'Selectable — offered in the create picker' },
    { value: 'mandatory', label: 'Mandatory — auto-applied to one item type' },
  ];

  let loading = $state(true);
  let templates = $state([]);
  let itemTypes = $state([]);

  // Modal state: closed, or open for create (editingId = null) / edit (id).
  let showModal = $state(false);
  let editingId = $state(null);
  let formName = $state('');
  let formBody = $state('');
  let formMode = $state('selectable');
  let formActive = $state(true);
  let formTargetTypeIds = $state([]);
  let saving = $state(false);

  let deleteDialogOpen = $state(false);
  let pendingDelete = $state(null); // { id, name }

  const itemTypeName = $derived.by(() => {
    const map = new Map(itemTypes.map((t) => [t.id, t.name]));
    return (id) => map.get(id) ?? `#${id}`;
  });

  function targetSummary(template) {
    if (!template.item_type_ids || template.item_type_ids.length === 0) return 'All types';
    return template.item_type_ids.map((id) => itemTypeName(id)).join(', ');
  }

  async function load() {
    loading = true;
    try {
      const [tmpls, types] = await Promise.all([
        api.itemTemplates.getAll({ workspace_id: workspaceId }),
        api.itemTypes.getAll(),
      ]);
      templates = tmpls ?? [];
      itemTypes = types ?? [];
    } catch (err) {
      console.error('Failed to load templates:', err);
      errorToast(err?.message || 'Failed to load templates');
    } finally {
      loading = false;
    }
  }
  onMount(load);

  function openCreate() {
    editingId = null;
    formName = '';
    formBody = '';
    formMode = 'selectable';
    formActive = true;
    formTargetTypeIds = [];
    showModal = true;
  }

  function openEdit(template) {
    editingId = template.id;
    formName = template.name;
    formBody = template.description_body || '';
    formMode = template.mode || 'selectable';
    formActive = template.is_active !== false;
    formTargetTypeIds = [...(template.item_type_ids ?? [])];
    showModal = true;
  }

  function closeModal() {
    showModal = false;
    editingId = null;
  }

  // A mandatory template must target exactly one item type (the server enforces
  // this too; we surface it inline so save is blocked with a clear reason).
  const mandatoryTypeError = $derived(
    formMode === 'mandatory' && formTargetTypeIds.length !== 1
      ? 'A mandatory template must target exactly one item type.'
      : ''
  );
  let canSave = $derived(!!formName.trim() && !mandatoryTypeError && !saving);

  async function save() {
    if (!canSave) return;
    const body = {
      name: formName.trim(),
      description_body: formBody,
      mode: formMode,
      is_active: formActive,
      item_type_ids: formTargetTypeIds,
      workspace_id: Number(workspaceId),
    };
    saving = true;
    try {
      if (editingId === null) {
        await api.itemTemplates.create(body);
        successToast('Template created');
      } else {
        await api.itemTemplates.update(editingId, body);
        successToast('Template updated');
      }
      closeModal();
      await load();
    } catch (err) {
      errorToast(err?.message || 'Failed to save template');
      console.error('Failed to save template:', err);
    } finally {
      saving = false;
    }
  }

  function openDeleteDialog(template) {
    pendingDelete = { id: template.id, name: template.name };
    deleteDialogOpen = true;
  }

  async function confirmDelete() {
    const target = pendingDelete;
    deleteDialogOpen = false;
    pendingDelete = null;
    if (!target) return;
    try {
      await api.itemTemplates.delete(target.id);
      successToast('Template deleted');
      await load();
    } catch (err) {
      errorToast(err?.message || 'Failed to delete template');
      console.error('Failed to delete template:', err);
    }
  }
</script>

<Panel padding="spacious">
  <SectionHeader
    title="Work item templates"
    subtitle="Reusable description scaffolds that pre-fill a new item's description — offered in the create picker, or enforced per item type."
  >
    {#snippet actions()}
      <Button
        size="sm"
        icon={Plus}
        onclick={openCreate}
        dataTestid="item-template-add"
        keyboardHint="A"
        hotkeyConfig={{ key: toHotkeyString('templates', 'add'), guard: () => !showModal }}
      >
        New template
      </Button>
    {/snippet}
  </SectionHeader>

  {#if loading}
    <div class="flex items-center justify-center py-6">
      <Loader2 class="w-5 h-5 animate-spin" style="color: var(--ds-icon-subtle);" />
    </div>
  {:else if templates.length === 0}
    <EmptyState
      icon={FileStack}
      title="No templates yet"
      description="Create one to give your team a consistent starting structure for new items."
    >
      {#snippet action()}
        <!-- shortcut-guard-exempt: duplicate of the section-header "New template" action in an admin settings section -->
        <Button size="sm" icon={Plus} onclick={openCreate}>New template</Button>
      {/snippet}
    </EmptyState>
  {:else}
    <div class="border rounded-md overflow-hidden" style="border-color: var(--ds-border);">
      <table class="w-full text-sm" data-testid="item-template-list">
        <thead>
          <tr style="background-color: var(--ds-background-neutral);">
            <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Name</th>
            <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Mode</th>
            <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Item types</th>
            <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Status</th>
            <th class="px-3 py-2"></th>
          </tr>
        </thead>
        <tbody>
          {#each templates as template (template.id)}
            <tr class="border-t" style="border-color: var(--ds-border);" data-testid="item-template-row">
              <td class="px-3 py-2 whitespace-nowrap" style="color: var(--ds-text);">{template.name}</td>
              <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{template.mode}</td>
              <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{targetSummary(template)}</td>
              <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{template.is_active ? 'active' : 'inactive'}</td>
              <td class="px-3 py-2 text-right whitespace-nowrap">
                <div class="flex items-center justify-end gap-2">
                  <Button variant="default" size="small" icon={Pencil} onclick={() => openEdit(template)} dataTestid="item-template-edit">
                    Edit
                  </Button>
                  <Button variant="default" size="small" icon={Trash2} onclick={() => openDeleteDialog(template)} dataTestid="item-template-delete">
                    Delete
                  </Button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Panel>

<Modal isOpen={showModal} onclose={closeModal} onSubmit={save} submitDisabled={!canSave} maxWidth="max-w-2xl">
  {#snippet children(submitHint)}
    <ModalHeader
      title={editingId === null ? 'New template' : 'Edit template'}
      icon={FileStack}
      onclose={closeModal}
    />
    <div class="px-6 py-4 space-y-3" data-testid="item-template-editor">
      <div class="grid grid-cols-2 gap-3">
        <div>
          <Label for="item-template-name" required class="mb-1">Name</Label>
          <Input id="item-template-name" bind:value={formName} placeholder="bug-report" dataTestid="item-template-name" />
        </div>
        <div>
          <Label for="item-template-mode" class="mb-1">Mode</Label>
          <Select id="item-template-mode" bind:value={formMode} options={MODE_OPTIONS} />
        </div>
      </div>

      <div>
        <Label class="mb-1">Target item types {formMode === 'selectable' ? '(optional — none means all)' : '(exactly one)'}</Label>
        <div data-testid="item-template-types">
          <BasePicker
            bind:value={formTargetTypeIds}
            items={itemTypes}
            multiple={true}
            maxSelections={formMode === 'mandatory' ? 1 : null}
            placeholder={formMode === 'selectable' ? 'All item types' : 'Select an item type'}
            getValue={(type) => type.id}
            getLabel={(type) => type.name}
            optionTestid={(opt) => `item-template-type-option-${opt.value}`}
          />
        </div>
        {#if mandatoryTypeError}
          <p class="mt-1 text-xs" style="color: var(--ds-text-danger);" data-testid="item-template-type-error">{mandatoryTypeError}</p>
        {/if}
      </div>

      <div>
        <Label class="mb-1">Description body (Markdown)</Label>
        <div class="border rounded-md" style="border-color: var(--ds-border);" data-testid="item-template-body">
          <MilkdownEditor bind:content={formBody} showToolbar={true} placeholder={'## Steps to reproduce\n\n1. ...'} />
        </div>
      </div>

      <span data-testid="item-template-active">
        <Checkbox bind:checked={formActive} label="Active" />
      </span>
    </div>
    <DialogFooter
      onCancel={closeModal}
      onConfirm={save}
      confirmLabel={editingId === null ? 'Create template' : 'Save changes'}
      disabled={!canSave}
      loading={saving}
      confirmTestid="item-template-save"
      showKeyboardHint
      confirmKeyboardHint={submitHint}
    />
  {/snippet}
</Modal>

<ConfirmDialog
  bind:show={deleteDialogOpen}
  variant="danger"
  title="Delete template?"
  message={`Delete the template "${pendingDelete?.name ?? ''}"? New items will no longer offer or enforce it; existing items are unaffected.`}
  confirmText="Delete template"
  onconfirm={confirmDelete}
  oncancel={() => (pendingDelete = null)}
/>
