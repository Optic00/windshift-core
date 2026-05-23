<script>
  import ExcalidrawEditor from '../../components/ExcalidrawEditor.svelte';
  import Button from '../../components/Button.svelte';
  import { api } from '../../api.js';
  import { themeStore } from '../../stores/theme.svelte.js';
  import { t } from '../../stores/i18n.svelte.js';
  import { errorToast } from '../../stores/toasts.svelte.js';
  import { confirm } from '../../composables/useConfirm.js';
  import { portal } from '../../actions/portal.js';

  let {
    open = $bindable(false),
    mode = 'create',                  // 'create' | 'edit'
    initialAttachmentId = null,
    initialName = '',
    pageId,
    onSaved = (_payload) => {},
  } = $props();

  let editorComponent = $state(null);
  let diagramName = $state('');
  let initialData = $state(null);
  let loadingSeed = $state(false);
  let saving = $state(false);
  let hasChanges = $state(false);
  let lastLoadedId = null;
  let initialElementCount = 0;

  $effect(() => {
    if (!open) {
      // Reset when modal closes so reopening triggers a fresh load.
      lastLoadedId = null;
      initialData = null;
      hasChanges = false;
      diagramName = '';
      initialElementCount = 0;
      return;
    }
    diagramName = initialName || t('editors.diagramUntitled');
    if (mode === 'edit' && initialAttachmentId && initialAttachmentId !== lastLoadedId) {
      lastLoadedId = initialAttachmentId;
      void loadAttachment(initialAttachmentId);
    } else if (mode === 'create') {
      initialData = { elements: [], appState: {}, files: {}, scrollToContent: true };
      initialElementCount = 0;
    }
  });

  async function loadAttachment(id) {
    loadingSeed = true;
    try {
      const res = await fetch(`/api/attachments/${id}/download`, { credentials: 'same-origin' });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const scene = await res.json();
      initialData = {
        elements: scene.elements || [],
        appState: scene.appState || {},
        files: scene.files || {},
        scrollToContent: true,
      };
      initialElementCount = (scene.elements || []).length;
    } catch (err) {
      console.error('Failed to load diagram for edit:', err);
      errorToast(t('editors.diagramLoadError'));
      initialData = { elements: [], appState: {}, files: {}, scrollToContent: true };
      initialElementCount = 0;
    } finally {
      loadingSeed = false;
    }
  }

  // Driven by scene-element count rather than onChange-event count: Excalidraw
  // fires several change events during mount (initial scene, theme apply,
  // resize observer) which would falsely flag hasChanges if we trusted them.
  function handleEditorChange(sceneData) {
    const count = sceneData?.elements?.length ?? 0;
    hasChanges = count !== initialElementCount;
  }

  async function handleSave() {
    if (!editorComponent || !pageId) return;
    saving = true;
    try {
      const sceneData = editorComponent.getSceneData();
      const blob = new Blob([JSON.stringify(sceneData)], { type: 'application/json' });
      const form = new FormData();
      const filename = `diagram-${Date.now()}.json`;
      form.append('file', blob, filename);
      form.append('entity_type', 'page');
      form.append('entity_id', String(pageId));
      const resp = await api.attachments.upload(form);
      const attachmentId = resp?.attachment?.id;
      if (!Number.isInteger(attachmentId)) {
        throw new Error('Upload response missing attachment id');
      }
      onSaved({ attachmentId, name: diagramName.trim() || t('editors.diagramUntitled') });
      open = false;
    } catch (err) {
      console.error('Failed to save diagram:', err);
      errorToast(t('editors.diagramSaveError'));
    } finally {
      saving = false;
    }
  }

  async function handleClose() {
    if (hasChanges) {
      const ok = await confirm({
        title: t('common.discardChanges'),
        message: t('editors.diagramUnsavedConfirm'),
        confirmText: t('common.discard'),
        cancelText: t('common.cancel'),
        variant: 'warning',
      });
      if (!ok) return;
    }
    open = false;
  }

  function handleKeyDown(event) {
    if (event.key === 'Escape') {
      event.stopPropagation();
      void handleClose();
    }
  }
</script>

{#if open}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    use:portal
    class="fixed inset-0 flex items-center justify-center z-[60]"
    style="background-color: rgba(0, 0, 0, 0.3); backdrop-filter: blur(2px);"
    onkeydown={handleKeyDown}
    data-testid="page-diagram-modal"
  >
    <div class="rounded shadow-xl w-full h-full max-w-[95vw] max-h-[95vh] flex flex-col" style="background-color: var(--ds-surface-raised);">
      <div class="flex items-center justify-between p-4 border-b" style="border-color: var(--ds-border);">
        <div class="flex items-center space-x-4 flex-1 min-w-0">
          <input
            type="text"
            bind:value={diagramName}
            placeholder={t('editors.diagramNamePlaceholder')}
            class="px-3 py-2 border rounded-md focus:outline-none focus:ring-2 focus:ring-blue-500 max-w-md"
            style="background-color: var(--ds-surface-raised); border-color: var(--ds-border); color: var(--ds-text);"
            data-testid="page-diagram-name"
          />
          {#if hasChanges}
            <span class="text-sm text-orange-600">{t('editors.diagramUnsaved')}</span>
          {/if}
        </div>
        <div class="flex items-center space-x-2 shrink-0">
          <Button variant="default" disabled={saving} onclick={handleClose}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="primary"
            disabled={saving || loadingSeed}
            loading={saving}
            onclick={handleSave}
            dataTestid="page-diagram-save"
          >
            {saving ? t('common.saving') : t('common.save')}
          </Button>
        </div>
      </div>
      <div class="flex-1 overflow-hidden">
        {#if loadingSeed}
          <div class="w-full h-full flex items-center justify-center">
            <span class="text-sm" style="color: var(--ds-text-muted);">{t('common.loading')}</span>
          </div>
        {:else}
          <ExcalidrawEditor
            bind:this={editorComponent}
            initialData={initialData}
            onChange={handleEditorChange}
            theme={themeStore.resolvedTheme}
          />
        {/if}
      </div>
    </div>
  </div>
{/if}
