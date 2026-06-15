<script>
  // Workspace agent-skills library (WI-258). Skills are markdown knowledge
  // packs ("how we write release notes", "our REST handler conventions")
  // attached to agent bindings. A run's prompt indexes the attached skills
  // by name + description; the agent reads a body on demand with
  // `ws skill get <id>` — so the description is the trigger, the body is
  // the content.

  import { onMount } from 'svelte';
  import { BookOpen, Loader2, Pencil, Plus, Trash2 } from '@lucide/svelte';
  import { agentSkills } from '../api.js';
  import Panel from '../components/Panel.svelte';
  import Button from '../components/Button.svelte';
  import Checkbox from '../components/Checkbox.svelte';
  import Input from '../components/Input.svelte';
  import Label from '../components/Label.svelte';
  import Textarea from '../components/Textarea.svelte';
  import SectionHeader from '../layout/SectionHeader.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Modal from '../dialogs/Modal.svelte';
  import ModalHeader from '../dialogs/ModalHeader.svelte';
  import DialogFooter from '../dialogs/DialogFooter.svelte';
  import ConfirmDialog from '../dialogs/ConfirmDialog.svelte';
  import { errorToast, successToast } from '../stores/toasts.svelte.js';
  import { toHotkeyString } from '../utils/keyboardShortcuts.js';

  // onchanged fires after any successful create/update/delete so the
  // bindings panel above can refresh its skill attach-pickers.
  let { workspaceId, onchanged = null } = $props();

  let loading = $state(true);
  let skills = $state([]);

  // Modal state: closed, or open for create (editingId = null) / edit (id).
  let showModal = $state(false);
  let editingId = $state(null);
  let formName = $state('');
  let formDescription = $state('');
  let formBody = $state('');
  let formEnabled = $state(true);
  let saving = $state(false);

  let deleteDialogOpen = $state(false);
  let pendingDelete = $state(null); // { id, name }

  async function load() {
    loading = true;
    try {
      skills = (await agentSkills.listForWorkspace(workspaceId)) ?? [];
    } catch (err) {
      console.error('Failed to load agent skills:', err);
      errorToast(err?.message || 'Failed to load agent skills');
    } finally {
      loading = false;
    }
  }
  onMount(load);

  function openCreate() {
    editingId = null;
    formName = '';
    formDescription = '';
    formBody = '';
    formEnabled = true;
    showModal = true;
  }

  function openEdit(skill) {
    editingId = skill.id;
    formName = skill.name;
    formDescription = skill.description || '';
    formBody = skill.body || '';
    formEnabled = !!skill.enabled;
    showModal = true;
  }

  function closeModal() {
    showModal = false;
    editingId = null;
  }

  let canSave = $derived(!!formName.trim() && !saving);

  async function save() {
    if (!canSave) return;
    const body = {
      name: formName.trim(),
      description: formDescription.trim(),
      body: formBody,
      enabled: formEnabled,
    };
    saving = true;
    try {
      if (editingId === null) {
        await agentSkills.create(workspaceId, body);
        successToast('Skill created');
      } else {
        await agentSkills.update(workspaceId, editingId, body);
        successToast('Skill updated');
      }
      closeModal();
      await load();
      onchanged?.();
    } catch (err) {
      errorToast(err?.message || 'Failed to save skill');
      console.error('Failed to save skill:', err);
    } finally {
      saving = false;
    }
  }

  function openDeleteDialog(skill) {
    pendingDelete = { id: skill.id, name: skill.name };
    deleteDialogOpen = true;
  }

  async function confirmDelete() {
    const target = pendingDelete;
    deleteDialogOpen = false;
    pendingDelete = null;
    if (!target) return;
    try {
      await agentSkills.remove(workspaceId, target.id);
      successToast('Skill deleted');
      await load();
      onchanged?.();
    } catch (err) {
      errorToast(err?.message || 'Failed to delete skill');
      console.error('Failed to delete skill:', err);
    }
  }
</script>

<Panel padding="spacious">
  <SectionHeader
    title="Agent skills"
    subtitle="Markdown knowledge packs your agents read on demand — attach them to a binding above."
  >
    {#snippet actions()}
      <Button
        size="sm"
        icon={Plus}
        onclick={openCreate}
        dataTestid="agent-skill-add"
        keyboardHint="N"
        hotkeyConfig={{ key: toHotkeyString('agentSkills', 'add'), guard: () => !showModal }}
      >
        New skill
      </Button>
    {/snippet}
  </SectionHeader>

  {#if loading}
    <div class="flex items-center justify-center py-6">
      <Loader2 class="w-5 h-5 animate-spin" style="color: var(--ds-icon-subtle);" />
    </div>
  {:else if skills.length === 0}
    <EmptyState
      icon={BookOpen}
      title="No skills yet"
      description="Create one to give your agents reusable, curated knowledge."
    >
      {#snippet action()}
        <!-- shortcut-guard-exempt: duplicate of the section-header "New skill" action, which carries the N hotkey -->
        <Button size="sm" icon={Plus} onclick={openCreate}>New skill</Button>
      {/snippet}
    </EmptyState>
  {:else}
    <div class="border rounded-md overflow-hidden" style="border-color: var(--ds-border);">
      <table class="w-full text-sm">
        <thead>
          <tr style="background-color: var(--ds-background-neutral);">
            <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Name</th>
            <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Description</th>
            <th class="text-left px-3 py-2 font-medium" style="color: var(--ds-text);">Status</th>
            <th class="px-3 py-2"></th>
          </tr>
        </thead>
        <tbody>
          {#each skills as skill (skill.id)}
            <tr class="border-t" style="border-color: var(--ds-border);">
              <td class="px-3 py-2 whitespace-nowrap" style="color: var(--ds-text);">{skill.name}</td>
              <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{skill.description || '—'}</td>
              <td class="px-3 py-2" style="color: var(--ds-text-subtle);">{skill.enabled ? 'enabled' : 'disabled'}</td>
              <td class="px-3 py-2 text-right whitespace-nowrap">
                <Button size="sm" variant="ghost" onclick={() => openEdit(skill)} title="Edit skill" dataTestid="agent-skill-edit">
                  <Pencil class="w-4 h-4" />
                </Button>
                <Button size="sm" variant="ghost" onclick={() => openDeleteDialog(skill)} title="Delete skill" dataTestid="agent-skill-delete">
                  <Trash2 class="w-4 h-4" style="color: var(--ds-text-danger);" />
                </Button>
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
      title={editingId === null ? 'New skill' : 'Edit skill'}
      icon={BookOpen}
      onclose={closeModal}
    />
    <div class="px-6 py-4 space-y-3" data-testid="agent-skill-editor">
      <div class="grid grid-cols-2 gap-3">
        <div>
          <Label for="agent-skill-name" required class="mb-1">Name</Label>
          <Input id="agent-skill-name" bind:value={formName} placeholder="release-notes" />
        </div>
        <div>
          <Label for="agent-skill-description" class="mb-1">Description (when should the agent reach for this?)</Label>
          <Input id="agent-skill-description" bind:value={formDescription} placeholder="How we write and format release notes" />
        </div>
      </div>
      <div>
        <Label for="agent-skill-body" class="mb-1">Body (markdown — the SKILL.md content)</Label>
        <Textarea
          id="agent-skill-body"
          bind:value={formBody}
          rows={10}
          size="small"
          class="font-mono"
          placeholder={'# Release notes\n\nStructure every release note as...'}
        />
      </div>
      <span data-testid="agent-skill-enabled">
        <Checkbox bind:checked={formEnabled} label="Enabled" />
      </span>
    </div>
    <DialogFooter
      onCancel={closeModal}
      onConfirm={save}
      confirmLabel={editingId === null ? 'Create skill' : 'Save changes'}
      disabled={!canSave}
      loading={saving}
      confirmTestid="agent-skill-save"
      showKeyboardHint
      confirmKeyboardHint={submitHint}
    />
  {/snippet}
</Modal>

<ConfirmDialog
  bind:show={deleteDialogOpen}
  variant="danger"
  title="Delete skill?"
  message={`Delete the skill "${pendingDelete?.name ?? ''}"? Bindings using it lose the attachment; runs already in flight keep the copy in their prompt.`}
  confirmText="Delete skill"
  onconfirm={confirmDelete}
  oncancel={() => (pendingDelete = null)}
/>
