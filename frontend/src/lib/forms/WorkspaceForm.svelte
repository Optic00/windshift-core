<script>
  import { t } from '../stores/i18n.svelte.js';
  import MilkdownEditor from '../editors/LazyMilkdownEditor.svelte';
  import Input from '../components/Input.svelte';
  import ChipPicker from '../pickers/ChipPicker.svelte';
  import Spinner from '../components/Spinner.svelte';
  import { LayoutTemplate } from '@lucide/svelte';

  let {
    formData = $bindable({
      name: '',
      key: '',
      description: '',
      template_workspace_id: null
    }),
    templates = [],
    templatesLoading = false,
    templatesError = null,
    nameInputRef = $bindable(null)
  } = $props();

  let keyManuallyEdited = false;

  function generateKey(name) {
    const words = name.trim().split(/\s+/).filter(Boolean);
    if (words.length === 0) return '';
    if (words.length === 1) {
      return words[0].substring(0, 2).toUpperCase();
    }
    return words.map(w => w[0]).join('').substring(0, 5).toUpperCase();
  }

  function onNameInput() {
    if (!keyManuallyEdited) {
      formData.key = generateKey(formData.name);
    }
  }

  function onKeyInput(e) {
    keyManuallyEdited = true;
    formData.key = e.target.value.toUpperCase();
  }

  let templatePickerItems = $derived.by(() => {
    if (templates.length === 0) return [];
    return [
      { id: null, name: t('createModal.workspaceTemplateBlank') },
      ...templates
    ];
  });

  export function validate() {
    return formData.name.trim() !== '' && formData.key.trim() !== '';
  }

  export function getFormData() {
    return {
      name: formData.name,
      key: formData.key,
      description: formData.description || '',
      active: true,
      template_workspace_id: formData.template_workspace_id ?? null
    };
  }

  export function reset() {
    formData = {
      name: '',
      key: '',
      description: '',
      template_workspace_id: null
    };
    keyManuallyEdited = false;
  }

  export function isValid() {
    return formData.name.trim() !== '' && formData.key.trim() !== '';
  }
</script>

<div class="space-y-3">
  <!-- Title Input -->
  <Input
    id="workspace-name"
    bind:inputRef={nameInputRef}
    bind:value={formData.name}
    oninput={onNameInput}
    type="text"
    variant="ghost"
    class="w-full text-lg font-medium border-0 outline-none bg-transparent"
    style="color: var(--ds-text);"
    placeholder={t('createModal.workspaceName', { type: t('createModal.workspace') })}
  />

  <!-- Workspace Key -->
  <Input
    id="workspace-key"
    bind:value={formData.key}
    oninput={onKeyInput}
    type="text"
    variant="ghost"
    class="w-full text-sm border-0 outline-none bg-transparent"
    style="color: var(--ds-text-subtle);"
    placeholder={t('createModal.workspaceKeyPlaceholder')}
  />

  <!-- Template picker: blank workspace is the default and preserves the
       legacy creation flow. -->
  {#if templatesLoading}
    <div
      data-testid="workspace-template-loading"
      class="flex items-center gap-2 text-sm px-2 py-1.5 rounded"
      style="color: var(--ds-text-subtle);"
    >
      <Spinner size="sm" />
      <span>{t('createModal.workspaceTemplateLoading')}</span>
    </div>
  {:else if templatesError}
    <div
      data-testid="workspace-template-error"
      class="text-sm px-2 py-1.5 rounded"
      style="color: var(--ds-text-danger, #dc2626);"
    >
      {t('createModal.workspaceTemplateError')}
    </div>
  {:else if templatePickerItems.length > 0}
    <div data-testid="workspace-template-section" class="pt-1">
      <ChipPicker
        value={formData.template_workspace_id}
        items={templatePickerItems}
        getValue={(tpl) => tpl.id}
        getLabel={(tpl) => tpl.name}
        icon={LayoutTemplate}
        placeholder={t('createModal.workspaceTemplate')}
        searchable={true}
        searchFields={['name']}
        testId="workspace-template-picker"
        onSelect={(tpl) => {
          formData.template_workspace_id = tpl?.id ?? null;
        }}
      >
        {#snippet itemSnippet({ item })}
          <LayoutTemplate size={14} style="color: var(--ds-text-subtle); flex-shrink: 0;" />
          <span class="truncate">{item.name}</span>
          {#if item.id !== null && item.id !== undefined}
            <span
              class="text-xs ml-auto pl-2 flex-shrink-0"
              style="color: var(--ds-text-subtle);"
            >
              {t('createModal.workspaceTemplateMeta', {
                templates: item.template_count ?? 0,
                items: item.item_count ?? 0
              })}
            </span>
          {/if}
        {/snippet}
      </ChipPicker>
    </div>
  {/if}

  <!-- Description -->
  <div class="min-h-[60px]">
    <MilkdownEditor
      bind:content={formData.description}
      placeholder={t('createModal.addDescription')}
      compact={true}
      showToolbar={false}
      readonly={false}
      itemId={null}
    />
  </div>
</div>
