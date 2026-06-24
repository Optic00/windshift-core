<script>
  // Workspace agent-skills library (WI-258). Skills are markdown knowledge
  // packs ("how we write release notes", "our REST handler conventions")
  // attached to agent bindings. A run's prompt indexes the attached skills
  // by name + description; the agent reads a body on demand with
  // `ws skill get <id>` — so the description is the trigger, the body is
  // the content.

  import { onMount } from 'svelte';
  import { BookOpen, FileText, Loader2, Pencil, Plus, Trash2, X } from '@lucide/svelte';
  import { agentSkills } from '../api.js';
  import Panel from '../components/Panel.svelte';
  import Button from '../components/Button.svelte';
  import Checkbox from '../components/Checkbox.svelte';
  import Input from '../components/Input.svelte';
  import Label from '../components/Label.svelte';
  import Textarea from '../components/Textarea.svelte';
  import SectionHeader from '../layout/SectionHeader.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import PagePicker from '../pickers/PagePicker.svelte';
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
  // Referenced workspace pages (WI-517): [{ id, title }]. Their current
  // markdown is inlined into the body the agent receives via `ws skill get`.
  let formPages = $state([]);
  // Bumping this remounts the page picker after each pick so it returns to an
  // empty search — single-select pickers otherwise keep the last label.
  let pagePickerNonce = $state(0);
  let pagePickerValue = $state(null);
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
    formPages = [];
    pagePickerValue = null;
    showModal = true;
  }

  function openEdit(skill) {
    editingId = skill.id;
    formName = skill.name;
    formDescription = skill.description || '';
    formBody = skill.body || '';
    formEnabled = !!skill.enabled;
    formPages = (skill.pages || []).map((p) => ({ id: p.id, title: p.title }));
    pagePickerValue = null;
    showModal = true;
  }

  function addPage(page) {
    if (page && !formPages.some((p) => p.id === page.id)) {
      formPages = [...formPages, { id: page.id, title: page.title }];
    }
    // Reset + remount the picker so the next pick starts from an empty search.
    pagePickerValue = null;
    pagePickerNonce += 1;
  }

  function removePage(id) {
    formPages = formPages.filter((p) => p.id !== id);
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
      page_ids: formPages.map((p) => p.id),
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
                <Button size="sm" variant="danger" icon={Trash2} onclick={() => openDeleteDialog(skill)} title="Delete skill" dataTestid="agent-skill-delete"></Button>
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
      subtitle="A markdown knowledge pack your agents read on demand."
      icon={BookOpen}
      onclose={closeModal}
    />
    <div class="px-6 py-5 space-y-5" data-testid="agent-skill-editor">
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-4">
        <div>
          <Label for="agent-skill-name" required class="mb-1">Name</Label>
          <Input id="agent-skill-name" bind:value={formName} placeholder="release-notes" />
          <p class="mt-1 text-xs text-[var(--ds-text-subtle)]">Short, hyphenated — how the agent refers to it.</p>
        </div>
        <div>
          <Label for="agent-skill-description" class="mb-1">Description</Label>
          <Input id="agent-skill-description" bind:value={formDescription} placeholder="How we write and format release notes" />
          <p class="mt-1 text-xs text-[var(--ds-text-subtle)]">When should the agent reach for this? Shown in its prompt index.</p>
        </div>
      </div>

      <div>
        <Label for="agent-skill-body" class="mb-1">Body</Label>
        <Textarea
          id="agent-skill-body"
          bind:value={formBody}
          rows={10}
          size="small"
          class="font-mono"
          placeholder={'# Release notes\n\nStructure every release note as...'}
        />
        <p class="mt-1 text-xs text-[var(--ds-text-subtle)]">Markdown — the SKILL.md content the agent reads on demand.</p>
      </div>

      <!-- Reference pages (WI-517): attached pages' current markdown is inlined
           into the body the agent receives, so a skill can be built from living
           workspace docs instead of pasted (stale) copies. -->
      <div class="pt-4 border-t border-[var(--ds-border)]" data-testid="agent-skill-pages">
        <Label class="mb-1">Reference pages</Label>
        <p class="mb-2 text-xs text-[var(--ds-text-subtle)]">
          Attach workspace pages — their current content is inlined into the body the agent receives.
        </p>
        {#if formPages.length > 0}
          <div class="flex flex-wrap gap-2 mb-2">
            {#each formPages as page (page.id)}
              <span
                class="inline-flex items-center gap-1.5 pl-2 pr-1 py-1 rounded-full text-xs font-medium bg-[var(--ds-background-neutral)] text-[var(--ds-text)]"
                data-testid="agent-skill-page-chip"
              >
                <FileText class="w-3.5 h-3.5 shrink-0 text-[var(--ds-text-subtle)]" />
                <span class="truncate max-w-[16rem]">{page.title || 'Untitled'}</span>
                <button
                  type="button"
                  onclick={() => removePage(page.id)}
                  class="p-0.5 rounded-full hover:bg-[var(--ds-background-neutral-hovered)] text-[var(--ds-text-subtle)]"
                  aria-label={`Remove ${page.title || 'page'}`}
                  data-testid="agent-skill-page-remove"
                >
                  <X class="w-3 h-3" />
                </button>
              </span>
            {/each}
          </div>
        {/if}
        {#key pagePickerNonce}
          <PagePicker
            id="agent-skill-page-picker"
            {workspaceId}
            bind:value={pagePickerValue}
            allowClear={false}
            placeholder="Search pages to attach…"
            onSelect={addPage}
          />
        {/key}
      </div>

      <div class="pt-4 border-t border-[var(--ds-border)]">
        <span data-testid="agent-skill-enabled">
          <Checkbox bind:checked={formEnabled} label="Enabled" />
        </span>
        <p class="mt-1 text-xs text-[var(--ds-text-subtle)]">Disabled skills stay in the library but aren't offered to agents.</p>
      </div>
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
