<script>
  import { onMount } from 'svelte';
  import { api } from '../api.js';
  import { errorToast } from '../stores/toasts.svelte.js';
  import { confirm } from '../composables/useConfirm.js';
  import { Plus, Edit, Trash2 } from '@lucide/svelte';
  import { IconUserStar as AgentIcon } from '@tabler/icons-svelte-runes';
  import { toHotkeyString } from '../utils/keyboardShortcuts.js';
  import Button from '../components/Button.svelte';
  import DataTable from '../components/DataTable.svelte';
  import ModalHeader from '../dialogs/ModalHeader.svelte';
  import PageHeader from '../layout/PageHeader.svelte';
  import Modal from '../dialogs/Modal.svelte';
  import DialogFooter from '../dialogs/DialogFooter.svelte';
  import Select from '../components/Select.svelte';
  import Textarea from '../components/Textarea.svelte';
  import Input from '../components/Input.svelte';
  import Checkbox from '../components/Checkbox.svelte';
  import Lozenge from '../components/Lozenge.svelte';
  import './settings-form.css';

  let entries = $state([]);
  let defaults = $state([]);
  let isLoading = $state(true);
  let error = $state(null);
  let editingId = $state(null);
  let showCreateForm = $state(false);
  let keyLocked = $state(false);

  let formData = $state({
    template_key: '',
    name: '',
    default_type: 'standard',
    instructions: '',
    enabled: true,
  });

  const profileTypeOptions = [
    { value: 'standard', label: 'Standard' },
    { value: 'coding', label: 'Coding' },
  ];

  const defaultOptions = $derived([
    { value: '', label: 'Start from… (new custom template)' },
    ...defaults.map((d) => ({ value: d.key, label: d.name })),
  ]);

  onMount(() => {
    loadEntries();
    loadDefaults();
  });

  async function loadEntries() {
    try {
      isLoading = true;
      error = null;
      entries = await api.agentTemplates.getAll();
    } catch (err) {
      error = 'Failed to load agent templates: ' + err.message;
    } finally {
      isLoading = false;
    }
  }

  async function loadDefaults() {
    try {
      defaults = await api.agentTemplates.defaults();
    } catch {
      // Non-fatal: admins can still create overrides by typing a key.
    }
  }

  function resetForm() {
    formData = {
      template_key: '',
      name: '',
      default_type: 'standard',
      instructions: '',
      enabled: true,
    };
    keyLocked = false;
  }

  function startCreate() {
    resetForm();
    editingId = null;
    showCreateForm = true;
  }

  // Seed the create form from a built-in default so the new override
  // overwrites that template. Overrides the default's name, type, and
  // instructions; blank fields fall back to the default.
  function onDefaultSelected() {
    const selected = defaults.find((d) => d.key === formData.template_key);
    if (!selected) {
      resetForm();
      return;
    }
    formData = {
      template_key: selected.key,
      name: selected.name,
      default_type: selected.default_type,
      instructions: selected.instructions || '',
      enabled: true,
    };
    keyLocked = true;
  }

  function startEdit(entry) {
    formData = {
      template_key: entry.template_key,
      name: entry.name,
      default_type: entry.default_type,
      instructions: entry.instructions || '',
      enabled: entry.enabled,
    };
    keyLocked = false;
    editingId = entry.id;
    showCreateForm = true;
  }

  function cancelEdit() {
    showCreateForm = false;
    editingId = null;
    keyLocked = false;
  }

  async function saveEntry() {
    try {
      if (!formData.template_key.trim()) {
        errorToast('Template key is required');
        return;
      }
      if (!formData.name.trim()) {
        errorToast('Name is required');
        return;
      }

      if (editingId) {
        await api.agentTemplates.update(editingId, {
          name: formData.name,
          default_type: formData.default_type,
          instructions: formData.instructions,
          enabled: formData.enabled,
        });
      } else {
        await api.agentTemplates.create({
          template_key: formData.template_key,
          name: formData.name,
          default_type: formData.default_type,
          instructions: formData.instructions,
          enabled: formData.enabled,
        });
      }

      await loadEntries();
      cancelEdit();
      error = null;
    } catch (err) {
      errorToast('Failed to save: ' + err.message);
    }
  }

  async function deleteEntry(id, name) {
    const confirmed = await confirm({
      title: 'Delete',
      message: `Are you sure you want to delete the override for "${name}"? This restores the default template.`,
      confirmText: 'Delete',
      cancelText: 'Cancel',
      variant: 'danger',
    });
    if (!confirmed) return;

    try {
      await api.agentTemplates.delete(id);
      await loadEntries();
      error = null;
    } catch (err) {
      error = err.message;
    }
  }

  const columns = [
    { key: 'template_key', label: 'Key' },
    { key: 'name', label: 'Name' },
    { key: 'default_type', label: 'Profile Type', slot: 'default_type' },
    { key: 'enabled', label: 'Enabled', slot: 'enabled' },
    { key: 'actions', label: 'Actions' },
  ];

  function buildDropdownItems(entry) {
    return [
      {
        id: 'edit',
        type: 'regular',
        icon: Edit,
        title: 'Edit',
        hoverClass: 'hover-bg',
        onClick: () => startEdit(entry),
      },
      {
        id: 'delete',
        type: 'regular',
        icon: Trash2,
        title: 'Delete',
        color: 'var(--ds-text-danger)',
        hoverClass: 'hover-danger',
        onClick: () => deleteEntry(entry.id, entry.name),
      },
    ];
  }
</script>

<PageHeader
  icon={AgentIcon}
  title="Agent Templates"
  subtitle="Override the Agent Studio creation catalog: template names, profile types, and instructions"
>
  {#snippet actions()}
    <Button
      variant="primary"
      icon={Plus}
      onclick={startCreate}
      disabled={isLoading}
      dataTestid="agent-template-add"
      keyboardHint="A"
      hotkeyConfig={{ key: toHotkeyString('agentTemplates', 'add'), guard: () => !showCreateForm }}
    >
      Add Template
    </Button>
  {/snippet}
</PageHeader>

{#if error}
  <div class="error">
    {error}
  </div>
{/if}

<DataTable
  columns={columns}
  data={entries}
  keyField="id"
  loading={isLoading}
  emptyMessage="No overrides configured yet. The Agent Studio falls back to the embedded defaults."
  emptyIcon={AgentIcon}
  actionItems={buildDropdownItems}
  actionTriggerTestid={(entry) => `agent-template-actions-${entry.id}`}
  rowAttrs={(entry) => ({
    'data-testid': `agent-template-row-${entry.id}`,
  })}
>
  {#snippet default_type(entry)}
    <Lozenge
      color={entry.default_type === 'coding' ? 'purple' : 'blue'}
      text={entry.default_type === 'coding' ? 'Coding' : 'Standard'}
    />
  {/snippet}

  {#snippet enabled(entry)}
    <Lozenge
      color={entry.enabled ? 'green' : 'gray'}
      text={entry.enabled ? 'Yes' : 'No'}
    />
  {/snippet}
</DataTable>

<Modal isOpen={showCreateForm} onclose={cancelEdit} onSubmit={saveEntry} maxWidth="max-w-2xl">
  <ModalHeader
    title={editingId ? 'Edit Template Override' : 'Add Template Override'}
    showCloseButton={false}
  />

  <div class="px-6 py-4">
    <form onsubmit={(e) => { e.preventDefault(); saveEntry(); }}>
      {#if !editingId}
        <div class="form-group">
          <label for="start_from_default">Start from default</label>
          <Select
            id="start_from_default"
            bind:value={formData.template_key}
            onchange={onDefaultSelected}
            options={defaultOptions}
            placeholder="Overwrite a built-in template or create a new one"
          />
          <span class="text-xs" style="color: var(--ds-text-subtlest);">
            Picking a default overwrites it with your edits; "Start from…" creates a brand-new template.
          </span>
        </div>
      {/if}

      <div class="form-group">
        <label for="template_key">Template Key</label>
        <Input
          id="template_key"
          placeholder="e.g. software_engineer"
          bind:value={formData.template_key}
          disabled={!!editingId || keyLocked}
          required
        />
        {#if keyLocked}
          <span class="text-xs" style="color: var(--ds-text-subtlest);">
            This override replaces the built-in "{formData.name}" template.
          </span>
        {/if}
      </div>

      <div class="form-group">
        <label for="name">Name</label>
        <Input
          id="name"
          placeholder="e.g. Software Engineer"
          bind:value={formData.name}
          required
        />
      </div>

      <div class="form-group">
        <label for="default_type">Profile Type</label>
        <Select
          id="default_type"
          bind:value={formData.default_type}
          options={profileTypeOptions}
          required
        />
      </div>

      <div class="form-group">
        <label for="instructions">Instructions</label>
        <Textarea
          id="instructions"
          placeholder="The system prompt / persona for this template"
          bind:value={formData.instructions}
          rows={4}
        />
      </div>

      <div class="form-group">
        <Checkbox
          bind:checked={formData.enabled}
          label="Enabled"
        />
      </div>
    </form>
  </div>

  <DialogFooter
    onCancel={cancelEdit}
    onConfirm={saveEntry}
    confirmLabel={editingId ? 'Update' : 'Create'}
    showKeyboardHint
  />
</Modal>