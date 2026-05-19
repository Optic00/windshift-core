<script>
  import { createCombobox, melt } from '@melt-ui/svelte';
  import { scale } from 'svelte/transition';
  import { backOut } from 'svelte/easing';
  import { api } from '../api.js';
  import { contextCommands } from '../utils/contextCommands.js';
  import { currentRoute } from '../router.js';
  import { permissionStore, workspacesStore, isSystemAdmin, currentWorkspace } from '../stores';
  import { moduleSettings } from '../stores/moduleSettings.js';
  import { timerStore } from '../stores/timerStore.svelte.js';
  import { t } from '../stores/i18n.svelte.js';
  import ModalBackdrop from '../components/ModalBackdrop.svelte';

  import { scoreCommand, compareCommands } from '../commands/score.js';
  import { BUCKET_LABELS, PER_BUCKET_CAP, TOTAL_CAP } from '../commands/buckets.js';
  import { deriveLegacyBucket } from '../commands/types.js';
  import { buildContext } from '../commands/context.js';
  import { buildCommands } from '../commands/buildCommands.js';
  import { executeCommand as runCommand } from '../commands/executor.js';
  import {
    adminProvider,
    createProvider,
    globalNavigationProvider,
    makeExternalProvider,
    searchProvider,
    systemProvider,
    timeProvider,
    workspaceActionsProvider,
    workspaceNavigationProvider,
    workspacesProvider,
  } from '../commands/providers/index.js';

  let {
    isOpen = $bindable(false),
    onclose,
  } = $props();

  let workspaces = $state([]);
  let workItems = $state([]);
  let searchTimeout;

  async function loadData() {
    try {
      const tasks = [];
      if (!$workspacesStore.loaded) tasks.push(workspacesStore.load());
      if (!$workspacesStore.personalWorkspace) tasks.push(workspacesStore.loadPersonalWorkspace());
      if (tasks.length) await Promise.all(tasks);
      workspaces = [
        ...($workspacesStore.personalWorkspace ? [$workspacesStore.personalWorkspace] : []),
        ...$workspacesStore.regularWorkspaces,
      ];
    } catch (err) {
      console.error('Failed to load data for command palette:', err);
    }
  }

  async function searchWorkItems(query) {
    if (!query || query.length < 2) {
      workItems = [];
      return;
    }
    try {
      const results = await api.search.items({ query: query.trim(), limit: 6 });
      workItems = results || [];
    } catch (err) {
      console.error('Failed to search work items:', err);
      workItems = [];
    }
  }

  function debouncedSearchWorkItems(query) {
    clearTimeout(searchTimeout);
    searchTimeout = setTimeout(() => searchWorkItems(query), 300);
  }

  const {
    elements: { menu, input, option },
    states: { open, inputValue, selected },
  } = createCombobox({
    forceVisible: true,
    portal: null,
  });

  $effect(() => {
    if (isOpen !== $open) open.set(isOpen);
  });

  $effect(() => {
    if ($inputValue && $inputValue.length >= 2) {
      debouncedSearchWorkItems($inputValue);
    } else if ($inputValue.length < 2) {
      workItems = [];
    }
  });

  // Provider order: focused-entity first, then workspace-context, then
  // global. Within-bucket ordering matches plan's bucket display order.
  // The external provider wraps registerContextCommands consumers (ItemDetail
  // today) so component-pushed commands fall into the item-actions bucket
  // by default.
  const PROVIDERS = [
    makeExternalProvider(() => $contextCommands),
    workspaceActionsProvider,
    workspaceNavigationProvider,
    globalNavigationProvider,
    workspacesProvider,
    createProvider,
    adminProvider,
    timeProvider,
    searchProvider,
    systemProvider,
  ];

  const commands = $derived(buildCommands(
    buildContext({
      route: $currentRoute,
      permissions: $permissionStore,
      isSystemAdmin: $isSystemAdmin,
      modules: $moduleSettings,
      workspaces,
      currentWorkspace: $currentWorkspace,
      workItems,
      activeTimer: timerStore.activeTimer,
      t,
      query: $inputValue,
    }),
    PROVIDERS,
  ));

  // Score, sort by (bucket, score, insertion), cap per-bucket and overall.
  // Providers set `bucket` explicitly; deriveLegacyBucket is the safety net
  // for commands flowing in through makeExternalProvider that haven't been
  // updated yet.
  function rankCommands(query, commandsList) {
    const annotated = commandsList.map((cmd, i) => {
      const label = cmd.label ?? '';
      const description = cmd.description ?? '';
      const keywords = cmd.keywords ?? [];
      const score = query.trim() ? scoreCommand(query, { label, description, keywords }) : 1;
      return {
        ...cmd,
        bucket: cmd.bucket || deriveLegacyBucket(cmd),
        _score: score,
        _seq: cmd._seq ?? i,
      };
    });

    const filtered = query.trim() ? annotated.filter((c) => c._score > 0) : annotated;
    filtered.sort(compareCommands(query));

    const counts = new Map();
    const out = [];
    for (const c of filtered) {
      if (out.length >= TOTAL_CAP) break;
      const n = counts.get(c.bucket) || 0;
      if (n >= PER_BUCKET_CAP) continue;
      counts.set(c.bucket, n + 1);
      out.push(c);
    }
    return out;
  }

  let filteredCommands = $derived(rankCommands($inputValue, commands));

  let userInteracted = $state(false);

  $effect(() => {
    if (filteredCommands.length > 0 && $inputValue.trim() && !userInteracted) {
      const first = filteredCommands[0];
      selected.set({ value: first.id, label: first.label });
    }
  });

  async function executeAndClose(cmd) {
    try {
      await runCommand(cmd);
    } catch (err) {
      console.error('[command-palette] execute failed:', err);
    } finally {
      close();
    }
  }

  function close() {
    isOpen = false;
    open.set(false);
    inputValue.set('');
    onclose?.();
  }

  function handleKeydown(e) {
    if (!isOpen) return;
    if (e.key === 'Enter' && $selected) {
      e.preventDefault();
      const cmd = filteredCommands.find((c) => c.id === $selected.value);
      if (cmd) executeAndClose(cmd);
    } else if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
      userInteracted = true;
    }
  }

  let searchInputRef = $state(null);

  $effect(() => {
    if (isOpen) {
      loadData();
      timerStore.initialize();
    }
  });

  $effect(() => {
    if (isOpen && searchInputRef) {
      setTimeout(() => {
        searchInputRef.focus();
        searchInputRef.select();
      }, 50);
    }
  });
</script>

<style>
  .command-palette-container {
    animation: scale-in var(--duration-normal, 200ms) var(--ease-spring, cubic-bezier(0.34, 1.56, 0.64, 1)) forwards;
  }

  [data-highlighted] {
    background-color: var(--ds-background-neutral-hovered) !important;
  }

  [data-melt-combobox-menu] {
    position: static !important;
    width: 100% !important;
    transform: none !important;
    top: auto !important;
    left: auto !important;
  }

  .command-option {
    transition: background-color var(--duration-fast, 100ms) ease;
  }

  .command-option:hover {
    background-color: var(--ds-background-neutral-hovered);
  }

  .bucket-header {
    padding: 0.5rem 1rem 0.25rem;
    font-size: 0.7rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    border-top: 1px solid;
  }
  .bucket-header:first-child {
    border-top: none;
  }

  .kbd {
    background-color: var(--ds-surface);
    color: var(--ds-text-subtle);
    transition: background-color var(--duration-fast, 100ms) ease;
  }

  .kbd:hover {
    background-color: var(--ds-background-neutral-hovered);
  }

  @media (prefers-reduced-motion: reduce) {
    .command-palette-container {
      animation: none;
    }
  }
</style>

<svelte:window onkeydown={handleKeydown} />

<ModalBackdrop bind:show={isOpen} opacity={0.4} blur={8} extraFilter="saturate(120%)" zIndex={60} align="top" paddingTop="pt-[20vh]" onclose={close}>
  <div
    class="relative w-full max-w-2xl mx-4"
    transition:scale={{ duration: 200, start: 0.95, easing: backOut }}
  >
    <div class="command-palette-container rounded-xl overflow-hidden" style="background-color: var(--ds-glass-bg, var(--ds-surface-raised)); backdrop-filter: blur(12px) saturate(180%); -webkit-backdrop-filter: blur(12px) saturate(180%); border: 1px solid var(--ds-glass-border, var(--ds-border)); box-shadow: var(--shadow-float, 0 20px 50px rgba(0, 0, 0, 0.18));">
      <div class="p-4 border-b" style="background-color: var(--ds-surface); border-color: var(--ds-border);">
        <input
          bind:this={searchInputRef}
          use:melt={$input}
          type="text"
          placeholder={t('commandPalette.searchPlaceholder')}
          class="w-full text-lg border-none outline-none bg-transparent"
          style="color: var(--ds-text);"
        />
      </div>

      {#if $open}
        <div
          use:melt={$menu}
          class="w-full"
          style="background-color: var(--ds-surface-raised);"
        >
          {#if filteredCommands.length === 0}
            <div class="p-4 text-center" style="color: var(--ds-text-subtle);">
              {t('commandPalette.noCommandsFound')}
            </div>
          {:else}
            <div class="max-h-96 overflow-y-auto">
              {#each filteredCommands as command, i}
                {#if i === 0 || filteredCommands[i - 1].bucket !== command.bucket}
                  <div class="bucket-header" style="color: var(--ds-text-subtle); background-color: var(--ds-surface); border-color: var(--ds-border);">
                    {BUCKET_LABELS[command.bucket] || ''}
                  </div>
                {/if}
                <div
                  use:melt={$option({ value: command.id, label: command.label })}
                  onclick={() => executeAndClose(command)}
                  class="w-full text-left px-4 py-2.5 transition-colors cursor-pointer command-option"
                >
                  <div class="flex items-center gap-2">
                    <div class="font-medium" style="color: var(--ds-text);">{command.label}</div>
                    {#if command._isContextCommand}
                      <span class="px-1.5 py-0.5 text-xs rounded font-medium" style="background-color: var(--ds-accent-blue-subtler); color: var(--ds-accent-blue);">
                        {t('commandPalette.context')}
                      </span>
                    {/if}
                  </div>
                  {#if command.description}
                    <div class="text-sm mt-0.5" style="color: var(--ds-text-subtle);">{command.description}</div>
                  {/if}
                </div>
              {/each}
            </div>
          {/if}

          <div class="p-3 border-t" style="background-color: var(--ds-surface); border-color: var(--ds-border);">
            <div class="flex justify-between text-xs mb-2" style="color: var(--ds-text-subtle);">
              <div>
                <kbd class="kbd px-1 py-0.5 rounded text-xs">↵</kbd> {t('commandPalette.toSelect')}
                <kbd class="kbd px-1 py-0.5 rounded text-xs ml-2">↑↓</kbd> {t('commandPalette.toNavigate')}
              </div>
              <div>
                <kbd class="kbd px-1 py-0.5 rounded text-xs">ESC</kbd> {t('commandPalette.toClose')}
              </div>
            </div>
            <div class="flex justify-between items-center">
              <button
                onclick={() => executeAndClose({ url: '/search' })}
                class="text-xs underline"
                style="color: var(--ds-interactive);"
              >
                {t('commandPalette.advancedSearch')}
              </button>
              <div class="text-xs" style="color: var(--ds-text-subtlest);">
                {t('commandPalette.pressToOpen', { shortcut: '⎵⎵' })}
              </div>
            </div>
          </div>
        </div>
      {/if}
    </div>
  </div>
</ModalBackdrop>
