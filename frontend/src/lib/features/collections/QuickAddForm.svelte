<script>
  import { Plus, X, Package, ChevronDown } from '@lucide/svelte';
  import Button from '../../components/Button.svelte';
  import { t } from '../../stores/i18n.svelte.js';
  import { itemTypeIconMap, workspaceIconMap } from '../../utils/icons.js';
  import {
    getDisplayString,
    getShortcut,
    matchesShortcut,
  } from '../../utils/keyboardShortcuts.js';
  const iconMap = { ...workspaceIconMap, ...itemTypeIconMap };
  const createShortcut = getShortcut('quickAdd', 'create');
  const cancelShortcut = getShortcut('quickAdd', 'cancel');

  let {
    parentId,
    formState,
    workspaces = [],
    compact = false,
    cardBgStyle = '',
    onUpdateField = () => {},
    onCreate = () => {},
    onCancel = () => {}
  } = $props();

  let selectedWorkspace = $derived(workspaces.find(w => w.id === formState.workspaceId));
  let selectedItemType = $derived(formState.availableTypes?.find(it => it.id === formState.itemTypeId));

  // Dropdown management
  let showWorkspaceDropdown = $state(false);
  let showItemTypeDropdown = $state(false);

  function handleKeydown(e) {
    if (matchesShortcut(e, createShortcut)) {
      e.preventDefault();
      onCreate(parentId);
    } else if (matchesShortcut(e, cancelShortcut)) {
      onCancel(parentId);
    }
  }

  function selectWorkspace(workspaceId) {
    onUpdateField(parentId, 'workspaceId', workspaceId);
    showWorkspaceDropdown = false;
  }

  function selectItemType(itemTypeId) {
    onUpdateField(parentId, 'itemTypeId', itemTypeId);
    showItemTypeDropdown = false;
  }
</script>

<div class="relative z-[100] rounded shadow-md border" style={cardBgStyle}>
  <!-- Textarea area -->
  <div class="p-3 pb-2">
    <textarea
      value={formState.title}
      data-quick-add-parent={parentId}
      oninput={(e) => onUpdateField(parentId, 'title', e.currentTarget.value)}
      onkeydown={handleKeydown}
      placeholder={t('collections.enterSummary')}
      rows="2"
      class="w-full px-0 py-0 text-sm resize-none border-0 focus:outline-none focus:ring-0"
      style="background-color: transparent; color: var(--ds-text); caret-color: var(--ds-text);"
    ></textarea>
  </div>

  <!-- Divider -->
  <div class="border-t mx-3" style="border-color: var(--ctx-border, var(--ds-border));"></div>

  <!-- Actions Footer -->
  <div class="p-3 pt-2 flex items-center gap-2" class:flex-wrap={!compact}>
    <div class="flex items-center gap-2" class:flex-wrap={!compact}>
      <!-- Workspace Selector -->
      <div class="relative">
        <Button
          variant="default"
          size="small"
          onclick={() => {
            showWorkspaceDropdown = !showWorkspaceDropdown;
            showItemTypeDropdown = false;
          }}
          class={compact ? '!size-7 !p-0' : '!size-[34px] !p-0'}
          title={selectedWorkspace?.name || 'Select workspace'}
        >
          {#if selectedWorkspace?.avatar_url}
            <img src={selectedWorkspace.avatar_url} alt="{selectedWorkspace.name} avatar" class="w-5 h-5 rounded object-cover" />
          {:else if selectedWorkspace?.icon}
            {@const WsIcon = iconMap[selectedWorkspace.icon] || Package}
            <WsIcon class="w-4 h-4" style="color: {selectedWorkspace?.color || 'var(--ds-icon)'};" />
          {:else}
            <Package class="w-4 h-4" style="color: var(--ds-icon);" />
          {/if}
        </Button>

        {#if showWorkspaceDropdown}
          <div
            class="absolute z-50 mt-1 w-48 rounded-md shadow-lg border py-1"
            style="background-color: var(--ds-surface-raised); border-color: var(--ds-border);"
          >
            {#each workspaces as ws}
              <button
                type="button"
                onclick={() => selectWorkspace(ws.id)}
                class="w-full px-3 py-2 text-left text-sm flex items-center gap-2 transition-colors"
                style="color: var(--ds-text);"
                onmouseenter={(e) => e.currentTarget.style.backgroundColor = 'var(--ds-background-selected)'}
                onmouseleave={(e) => e.currentTarget.style.backgroundColor = 'transparent'}
              >
                {#if ws.avatar_url}
                  <img src={ws.avatar_url} alt="" class="w-5 h-5 rounded object-cover" />
                {:else}
                  {@const WsDropdownIcon = iconMap[ws.icon] || Package}
                  <div
                    class="w-5 h-5 rounded flex items-center justify-center"
                    style="background-color: {ws.color || 'var(--ds-interactive)'};"
                  >
                    <WsDropdownIcon class="w-3 h-3 text-white" />
                  </div>
                {/if}
                <span class="truncate">{ws.name}</span>
              </button>
            {/each}
          </div>
        {/if}
      </div>

      <!-- Item Type Selector -->
      {#if formState.availableTypes?.length > 0}
        <div class="relative">
          {#if compact}
            <Button
              variant="default"
              size="small"
              onclick={() => {
                showItemTypeDropdown = !showItemTypeDropdown;
                showWorkspaceDropdown = false;
              }}
              class="!size-7 !p-0"
              title={selectedItemType?.name || 'Select type'}
            >
              {#if selectedItemType}
                {@const SelectedTypeIcon = iconMap[selectedItemType.icon] || Package}
                <SelectedTypeIcon class="w-4 h-4" style="color: {selectedItemType.color};" />
              {:else}
                <Package class="w-4 h-4" style="color: var(--ds-icon);" />
              {/if}
            </Button>
          {:else}
            <Button
              variant="default"
              size="small"
              onclick={() => {
                showItemTypeDropdown = !showItemTypeDropdown;
                showWorkspaceDropdown = false;
              }}
              class="!px-2.5"
              title={selectedItemType?.name || 'Select type'}
            >
              {#if selectedItemType}
                {@const SelectedTypeSmallIcon = iconMap[selectedItemType.icon] || Package}
                <div
                  class="w-5 h-5 rounded flex items-center justify-center"
                  style="background-color: {selectedItemType.color};"
                >
                  <SelectedTypeSmallIcon class="w-3 h-3 text-white" />
                </div>
                <span>{selectedItemType.name}</span>
              {:else}
                <span style="color: var(--ds-text-subtle);">{t('collections.selectType')}</span>
              {/if}
              <ChevronDown class="w-3.5 h-3.5" style="color: var(--ds-text-subtle);" />
            </Button>
          {/if}

          {#if showItemTypeDropdown}
            <div
              class="absolute z-50 mt-1 w-48 rounded-md shadow-lg border py-1"
              style="background-color: var(--ds-surface-raised); border-color: var(--ds-border);"
            >
              {#each formState.availableTypes as itemType}
                {@const TypeDropdownIcon = iconMap[itemType.icon] || Package}
                <button
                  type="button"
                  onclick={() => selectItemType(itemType.id)}
                  class="w-full px-3 py-2 text-left text-sm flex items-center gap-2 transition-colors"
                  style="color: var(--ds-text);"
                  onmouseenter={(e) => e.currentTarget.style.backgroundColor = 'var(--ds-background-selected)'}
                  onmouseleave={(e) => e.currentTarget.style.backgroundColor = 'transparent'}
                >
                  <div
                    class="w-5 h-5 rounded flex items-center justify-center"
                    style="background-color: {itemType.color};"
                  >
                    <TypeDropdownIcon class="w-3 h-3 text-white" />
                  </div>
                  <span class="truncate">{itemType.name}</span>
                </button>
              {/each}
            </div>
          {/if}
        </div>
      {/if}

      <!-- Create Button -->
      {#if compact}
        <!-- shortcut-guard-exempt: Enter is handled by the quick-add textarea; the compact icon-only layout intentionally omits its visible hint. -->
        <Button
          variant="primary"
          size="small"
          icon={Plus}
          onclick={() => onCreate(parentId)}
          class="!size-7 !p-0"
          title={t('common.create')}
        />
      {:else}
        <Button
          variant="primary"
          size="small"
          keyboardHint={getDisplayString(createShortcut)}
          onclick={() => onCreate(parentId)}
        >
          {t('common.create')}
        </Button>
      {/if}

      <!-- Cancel Button -->
      {#if compact}
        <Button
          variant="ghost"
          size="small"
          icon={X}
          onclick={() => onCancel(parentId)}
          class="!size-7 !p-0"
          title={t('common.cancel')}
        />
      {:else}
        <Button
          variant="ghost"
          size="small"
          keyboardHint={getDisplayString(cancelShortcut)}
          onclick={() => onCancel(parentId)}
        >
          {t('common.cancel')}
        </Button>
      {/if}
    </div>
  </div>

  <!-- Error message -->
  {#if formState.error}
    <div class="px-3 pb-3 text-xs" style="color: var(--ds-text-danger);">
      {formState.error}
    </div>
  {/if}
</div>
