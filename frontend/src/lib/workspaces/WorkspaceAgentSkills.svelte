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
  import Input from '../components/Input.svelte';
  import ConfirmDialog from '../dialogs/ConfirmDialog.svelte';
  import { errorToast, successToast } from '../stores/toasts.svelte.js';

  let { workspaceId } = $props();

  let loading = $state(true);
  let skills = $state([]);

  // Editor state: null = closed, 0 = creating, >0 = editing that id.
  let editorFor = $state(null);
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
    editorFor = 0;
    formName = '';
    formDescription = '';
    formBody = '';
    formEnabled = true;
  }

  function openEdit(skill) {
    editorFor = skill.id;
    formName = skill.name;
    formDescription = skill.description || '';
    formBody = skill.body || '';
    formEnabled = !!skill.enabled;
  }

  function closeEditor() {
    editorFor = null;
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
      if (editorFor === 0) {
        await agentSkills.create(workspaceId, body);
        successToast('Skill created');
      } else {
        await agentSkills.update(workspaceId, editorFor, body);
        successToast('Skill updated');
      }
      closeEditor();
      await load();
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
    } catch (err) {
      errorToast(err?.message || 'Failed to delete skill');
      console.error('Failed to delete skill:', err);
    }
  }
</script>

<Panel padding="spacious">
  <div class="flex items-center justify-between mb-1">
    <h4 class="text-sm font-medium flex items-center gap-2" style="color: var(--ds-text);">
      <BookOpen class="w-4 h-4" style="color: var(--ds-icon-subtle);" />
      Agent skills
    </h4>
    <button
      type="button"
      onclick={openCreate}
      class="inline-flex items-center gap-1 text-xs px-2 py-1 rounded border hover:opacity-80"
      style="border-color: var(--ds-border); color: var(--ds-text);"
      data-testid="agent-skill-add"
    >
      <Plus class="w-3.5 h-3.5" /> New skill
    </button>
  </div>
  <p class="text-xs mb-3" style="color: var(--ds-text-subtle);">
    Markdown knowledge packs for your agents. Attach skills to a binding below; the agent sees each
    skill's name and description in its prompt and reads the full body only when relevant. Keep the
    description as a "when to use this" trigger.
  </p>

  {#if loading}
    <div class="flex items-center justify-center py-6">
      <Loader2 class="w-5 h-5 animate-spin" style="color: var(--ds-icon-subtle);" />
    </div>
  {:else if skills.length === 0 && editorFor === null}
    <p class="text-sm py-2" style="color: var(--ds-text-subtle);">
      No skills yet. Create one to give your agents reusable, curated knowledge.
    </p>
  {:else if skills.length > 0}
    <div class="border rounded-md overflow-hidden mb-3" style="border-color: var(--ds-border);">
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
                <button
                  type="button"
                  onclick={() => openEdit(skill)}
                  class="inline-flex items-center justify-center p-1 rounded hover:opacity-80"
                  style="color: var(--ds-icon);"
                  title="Edit skill"
                  aria-label="Edit skill {skill.name}"
                  data-testid="agent-skill-edit"
                >
                  <Pencil class="w-4 h-4" />
                </button>
                <button
                  type="button"
                  onclick={() => openDeleteDialog(skill)}
                  class="inline-flex items-center justify-center p-1 rounded hover:opacity-80"
                  style="color: var(--ds-icon-danger);"
                  title="Delete skill"
                  aria-label="Delete skill {skill.name}"
                  data-testid="agent-skill-delete"
                >
                  <Trash2 class="w-4 h-4" />
                </button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if editorFor !== null}
    <div class="border rounded-md p-3 space-y-3" style="border-color: var(--ds-border);" data-testid="agent-skill-editor">
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="block text-xs mb-1" style="color: var(--ds-text-subtle);" for="agent-skill-name">Name</label>
          <Input id="agent-skill-name" bind:value={formName} placeholder="release-notes" />
        </div>
        <div>
          <label class="block text-xs mb-1" style="color: var(--ds-text-subtle);" for="agent-skill-description">
            Description (when should the agent reach for this?)
          </label>
          <Input id="agent-skill-description" bind:value={formDescription} placeholder="How we write and format release notes" />
        </div>
      </div>
      <div>
        <label class="block text-xs mb-1" style="color: var(--ds-text-subtle);" for="agent-skill-body">
          Body (markdown — the SKILL.md content)
        </label>
        <textarea
          id="agent-skill-body"
          bind:value={formBody}
          rows="10"
          class="w-full text-sm rounded border px-2 py-1 font-mono"
          style="border-color: var(--ds-border); background-color: var(--ds-background-input, transparent); color: var(--ds-text);"
          placeholder="# Release notes&#10;&#10;Structure every release note as..."
        ></textarea>
      </div>
      <div class="flex items-center justify-between">
        <label class="inline-flex items-center gap-2 text-sm" style="color: var(--ds-text);">
          <input type="checkbox" bind:checked={formEnabled} />
          Enabled
        </label>
        <div class="flex items-center gap-2">
          <button
            type="button"
            onclick={closeEditor}
            class="text-sm px-3 py-1 rounded border hover:opacity-80"
            style="border-color: var(--ds-border); color: var(--ds-text-subtle);"
          >
            Cancel
          </button>
          <button
            type="button"
            onclick={save}
            disabled={!canSave}
            class="text-sm px-3 py-1 rounded border hover:opacity-80 disabled:opacity-40 disabled:cursor-not-allowed"
            style="border-color: var(--ds-border); color: var(--ds-text);"
            data-testid="agent-skill-save"
          >
            {#if saving}<Loader2 class="w-3.5 h-3.5 animate-spin inline" />{/if}
            {editorFor === 0 ? 'Create skill' : 'Save changes'}
          </button>
        </div>
      </div>
    </div>
  {/if}
</Panel>

<ConfirmDialog
  bind:show={deleteDialogOpen}
  variant="danger"
  title="Delete skill?"
  message={`Delete the skill "${pendingDelete?.name ?? ''}"? Bindings using it lose the attachment; runs already in flight keep the copy in their prompt.`}
  confirmText="Delete skill"
  onconfirm={confirmDelete}
  oncancel={() => (pendingDelete = null)}
/>
