<script>
  import { createCombobox, melt } from '@melt-ui/svelte';
  import { fly } from 'svelte/transition';
  import { Check, ChevronDown, X, Search } from '@lucide/svelte';
  import Spinner from '../components/Spinner.svelte';
  import { t } from '../stores/i18n.svelte.js';

  let {
    // Core props
    value = $bindable(null),
    items = [],
    loading = false,
    error = null,
    disabled = false,

    // Display props
    id = undefined,
    placeholder = '',
    label = '',
    class: className = '',

    // Feature toggles
    allowClear = false,
    showUnassigned = false,
    unassignedLabel = '',
    multiple = false,
    showSelectedInTrigger = true,

    // Item configuration
    searchFields = ['name'],
    getValue = (item) => item?.id,
    getLabel = (item) => item?.name ?? '',

    // Snippets for customization
    itemSnippet = null,
    triggerSnippet = null,
    iconSnippet = null,
    chipSnippet = null,
    noResultsSnippet = null,
    createOptionSnippet = null,
    children = null,    // popover mode: custom trigger content
    footer = null,      // popover mode: rendered below items in dropdown

    // Create functionality
    allowCreate = false,
    onCreate = null,

    // Server-side search
    serverSearch = false,
    onSearchChange = null,

    // Popover mode: open on mount
    autoOpen = false,

    // Multi-select values bindable (used in popover mode)
    values = $bindable([]),

    // Event callbacks
    onSelect = () => {},
    onCancel = () => {},
    onChange = () => {}
  } = $props();

  // Popover mode: when a children snippet is provided the trigger is custom
  // and the search input lives inside the dropdown.
  const popoverMode = $derived(children != null);

  const resolvedPlaceholder = $derived(placeholder || t('pickers.select'));
  const resolvedUnassignedLabel = $derived(unassignedLabel || t('pickers.unassigned'));

  // Expose input value for create functionality
  export function getInputValue() {
    return $inputValue;
  }

  // Create Melt combobox
  const {
    elements: { menu, input, option, label: labelEl },
    states: { open, inputValue, touchedInput, selected },
    helpers: { isSelected }
  } = createCombobox({
    forceVisible: true,
    preventScroll: false,
    multiple: false, // We handle multi-select manually
    positioning: {
      strategy: 'fixed',
      placement: 'bottom-start',
      sameWidth: false
    },
    portal: 'body'
  });

  // In popover mode we maintain our own search term inside the dropdown.
  let popoverSearchTerm = $state('');

  // Debounced server-side search
  let debounceTimer;
  $effect(() => {
    if (!serverSearch || !onSearchChange) return;
    const query = popoverMode ? popoverSearchTerm : $inputValue;
    clearTimeout(debounceTimer);
    if (!popoverMode && !$touchedInput) return;
    debounceTimer = setTimeout(() => {
      onSearchChange(query || '');
    }, 300);
    return () => clearTimeout(debounceTimer);
  });

  // Filter items based on search input
  const filteredItems = $derived.by(() => {
    if (serverSearch) return items;

    const query = popoverMode ? popoverSearchTerm : $inputValue;
    if (!query) return items;

    const search = query.toLowerCase();
    return items.filter(item =>
      searchFields.some(field => {
        const fieldValue = typeof field === 'function' ? field(item) : item[field];
        return fieldValue?.toString().toLowerCase().includes(search);
      })
    );
  });

  // Create options for Melt combobox
  const options = $derived.by(() => {
    const opts = filteredItems.map(item => ({
      value: getValue(item),
      label: getLabel(item),
      item: item,
      isUnassigned: false
    }));

    if (showUnassigned && !multiple) {
      opts.unshift({
        value: null,
        label: resolvedUnassignedLabel,
        item: null,
        isUnassigned: true
      });
    }

    return opts;
  });

  // For multi-select: get array of selected items
  const selectedItems = $derived.by(() => {
    if (!multiple) return [];
    const valueArray = popoverMode ? (Array.isArray(values) ? values : []) : (Array.isArray(value) ? value : []);
    return valueArray
      .map(v => items.find(item => getValue(item) === v))
      .filter(Boolean);
  });

  // Track highlighted index for keyboard navigation
  let highlightedIndex = $state(0);

  // Check if an item is selected (multi-select)
  function isItemSelected(itemValue) {
    if (popoverMode && multiple) {
      return Array.isArray(values) && values.includes(itemValue);
    }
    if (!multiple) return value === itemValue;
    return Array.isArray(value) && value.includes(itemValue);
  }

  // Set display value when value changes externally (single-select, combobox mode)
  $effect(() => {
    if (!multiple && !$touchedInput && !popoverMode) {
      if (value != null && showSelectedInTrigger) {
        const item = items.find(i => getValue(i) === value);
        if (item) {
          $inputValue = getLabel(item);
        }
      } else {
        $inputValue = '';
      }
    }
  });

  // Auto-open
  $effect(() => {
    if (autoOpen) {
      $open = true;
    }
  });

  function getCreateQuery() {
    return (popoverMode ? popoverSearchTerm : ($inputValue || '')).trim();
  }

  function canCreateCurrentInput() {
    const query = getCreateQuery().toLowerCase();
    if (!allowCreate || !onCreate || query.length === 0) return false;
    return !options.some((opt) => (opt.label ?? '').trim().toLowerCase() === query);
  }

  async function handleCreateOption() {
    const query = getCreateQuery();
    if (!canCreateCurrentInput()) return;
    await onCreate?.(query);
    popoverSearchTerm = '';
    $open = false;
  }

  // Perform selection on an option
  function selectOption(opt) {
    if (multiple) {
      const itemValue = opt.value;
      if (isItemSelected(itemValue)) {
        if (popoverMode) {
          values = (values || []).filter(v => v !== itemValue);
        } else {
          value = (value || []).filter(v => v !== itemValue);
        }
      } else {
        if (popoverMode) {
          values = [...(values || []), itemValue];
        } else {
          value = [...(value || []), itemValue];
        }
      }
      if (popoverMode) {
        popoverSearchTerm = '';
        onChange(values);
      } else {
        $inputValue = '';
        onChange(value);
      }
    } else {
      value = opt.value;
      if (!popoverMode) {
        $inputValue = opt.isUnassigned ? '' : opt.label;
      }
      onSelect(opt.item);
    }
    $open = false;
  }

  // Handle keyboard navigation
  async function handleKeydown(event) {
    if (event.key === 'Escape') {
      event.preventDefault();
      onCancel();
      return;
    }

    if (event.key === 'Tab') {
      $open = false;
      return;
    }

    if (!$open) return;

    const totalItems = options.length;

    if (event.key === 'ArrowDown') {
      if (totalItems === 0) return;
      event.preventDefault();
      event.stopPropagation();
      highlightedIndex = (highlightedIndex + 1) % totalItems;
    } else if (event.key === 'ArrowUp') {
      if (totalItems === 0) return;
      event.preventDefault();
      event.stopPropagation();
      highlightedIndex = highlightedIndex === 0 ? totalItems - 1 : highlightedIndex - 1;
    } else if (event.key === 'Enter' || (event.key === ' ' && event.target.tagName !== 'INPUT')) {
      event.preventDefault();
      event.stopPropagation();

      if (event.key === 'Enter' && totalItems === 0 && canCreateCurrentInput()) {
        await handleCreateOption();
        return;
      }

      if (highlightedIndex >= 0 && highlightedIndex < totalItems) {
        selectOption(options[highlightedIndex]);
      }
    }
  }

  // Handle dropdown close without selection (single-select only)
  let wasOpen = $state(false);
  $effect(() => {
    if (wasOpen && !$open && !$selected && !multiple) {
      onCancel();
    }
    if (!wasOpen && $open) {
      highlightedIndex = 0;
      if (popoverMode) {
        popoverSearchTerm = '';
        setTimeout(() => searchInputRef?.focus(), 50);
      }
    }
    wasOpen = $open;
  });

  // Reset highlighted index when options change
  $effect(() => {
    const len = options.length;
    if (highlightedIndex >= len) {
      highlightedIndex = Math.max(0, len - 1);
    }
  });

  // Clear selection
  function handleClear(e) {
    e.stopPropagation();
    if (multiple) {
      if (popoverMode) {
        values = [];
        onChange([]);
      } else {
        value = [];
        onChange([]);
      }
    } else {
      value = null;
      if (!popoverMode) {
        $inputValue = '';
        $selected = null;
      }
      onSelect(null);
    }
  }

  // Remove a single item (multi-select)
  function removeItem(e, itemValue) {
    e.stopPropagation();
    if (popoverMode) {
      values = (values || []).filter(v => v !== itemValue);
      onChange(values);
    } else {
      value = (value || []).filter(v => v !== itemValue);
      onChange(value);
    }
  }

  // Focus input and open dropdown (combobox mode)
  let inputRef = $state(null);
  function focusInput() {
    inputRef?.focus();
    $open = true;
  }

  // Search input inside popover dropdown
  let searchInputRef = $state(null);

  // Reference to dropdown menu for scrolling
  let menuRef = $state(null);

  // Scroll highlighted item into view
  $effect(() => {
    if ($open && menuRef && options.length > 0) {
      const highlightedEl = menuRef.children[highlightedIndex];
      if (highlightedEl) {
        highlightedEl.scrollIntoView({ block: 'nearest' });
      }
    }
  });

  // Popover mode trigger handler
  function handleTriggerClick() {
    if (disabled) return;
    $open = !$open;
  }
</script>

<div class="relative {className}">
  {#if label}
    <label use:melt={$labelEl} class="block text-sm font-medium mb-1" style="color: var(--ds-text);">
      {label}
    </label>
  {/if}

  {#if popoverMode}
    <!-- Popover mode: custom trigger (children) toggles dropdown -->
    <div>
      <input use:melt={$input} type="hidden" aria-hidden="true" />
      <div
        role="combobox"
        aria-expanded={$open}
        aria-haspopup="listbox"
        aria-disabled={disabled}
        onclick={handleTriggerClick}
        onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleTriggerClick(); } }}
      >
        {@render children()}
      </div>
    </div>
  {:else if multiple}
    <!-- Multi-select: Container with chips + input (original behavior) -->
    <div
      class="w-full min-h-[38px] px-2.5 py-1.5 pr-10 rounded border transition-all duration-200
             focus-within:outline-none focus-within:ring-2 focus-within:ring-blue-500 focus-within:ring-opacity-50
             disabled:opacity-50 disabled:cursor-not-allowed flex flex-wrap items-center gap-1.5"
      style="background-color: var(--ds-background-input); border-color: var(--ds-border);"
      onclick={focusInput}
      onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && focusInput()}
      role="button" tabindex="-1"
    >
      {#each selectedItems as item (getValue(item))}
        <div class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-xs border"
             style="background-color: var(--ds-surface-raised); border-color: var(--ds-border); color: var(--ds-text);">
          {#if chipSnippet}
            {@render chipSnippet({ item })}
          {:else}
            <span class="font-medium truncate max-w-[150px]">{getLabel(item)}</span>
          {/if}
          <button type="button" onclick={(e) => removeItem(e, getValue(item))}
                  class="rounded p-0.5 transition-colors" {disabled}
                  onmouseenter={(e) => e.currentTarget.style.backgroundColor = 'var(--ds-background-neutral-hovered)'}
                  onmouseleave={(e) => e.currentTarget.style.backgroundColor = 'transparent'}>
            <X class="w-3 h-3" style="color: var(--ds-text-subtle);" />
          </button>
        </div>
      {/each}
      <input bind:this={inputRef} use:melt={$input} type="text"
             placeholder={selectedItems.length === 0 ? resolvedPlaceholder : ''}
             {disabled} onkeydowncapture={handleKeydown}
             class="flex-1 min-w-[120px] px-1 py-0.5 bg-transparent border-0 outline-none text-sm"
             style="color: var(--ds-text);" />
    </div>
    <div class="absolute right-2 top-1/2 transform -translate-y-1/2 flex items-center gap-1 pointer-events-none">
      {#if loading}<Spinner size="sm" />
      {:else}<ChevronDown size={16} class="transition-transform duration-200 {$open ? 'rotate-180' : ''}" style="color: var(--ds-text-subtle);" />{/if}
    </div>
  {:else}
    <!-- Single-select: Input/Trigger (original combobox mode) -->
    <input use:melt={$input} {id} type="text" placeholder={resolvedPlaceholder} {disabled}
           onkeydowncapture={handleKeydown}
           class="w-full px-4 py-2 pr-16 rounded border transition-all duration-200
                  focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-opacity-50
                  disabled:opacity-50 disabled:cursor-not-allowed text-sm"
           style="background-color: var(--ds-background-input); border-color: var(--ds-border); color: var(--ds-text);" />
    <div class="absolute right-2 top-1/2 transform -translate-y-1/2 flex items-center gap-1">
      {#if allowClear && value != null && !disabled && showSelectedInTrigger}
        <button type="button" onclick={handleClear}
                class="p-0.5 rounded transition-colors" style="color: var(--ds-text-subtle);"
                aria-label={t('pickers.clearSelection')}
                onmouseenter={(e) => e.currentTarget.style.backgroundColor = 'var(--ds-background-neutral-hovered)'}
                onmouseleave={(e) => e.currentTarget.style.backgroundColor = 'transparent'}>
          <X size={14} />
        </button>
      {/if}
      {#if loading}<Spinner size="sm" />
      {:else}<div class="pointer-events-none"><ChevronDown size={16} class="transition-transform duration-200 {$open ? 'rotate-180' : ''}" style="color: var(--ds-text-subtle);" /></div>{/if}
    </div>
  {/if}

  <!-- Dropdown Menu -->
  {#if $open}
    <div bind:this={menuRef} use:melt={$menu}
         class="fixed z-[70] min-w-[250px] rounded border shadow-lg overflow-hidden"
         style="background-color: var(--ds-surface-raised); border-color: var(--ds-border);"
         transition:fly={{ duration: 150, y: -5 }}>
      {#if popoverMode}
        <!-- Search input inside dropdown -->
        <div class="p-2 border-b" style="border-color: var(--ds-border);">
          <div class="relative">
            <Search size={14} class="absolute left-2.5 top-1/2 -translate-y-1/2" style="color: var(--ds-text-subtle);" />
            <input bind:this={searchInputRef} bind:value={popoverSearchTerm} type="text"
                   placeholder={t('pickers.search')}
                   onkeydown={handleKeydown}
                   class="w-full pl-8 pr-3 py-2 rounded text-sm outline-none"
                   style="background-color: var(--ds-background-input); border: 1px solid var(--ds-border); color: var(--ds-text);"
                   aria-autocomplete="list" />
          </div>
        </div>
      {/if}

      {#if loading}
        <div class="p-4 text-center" style="color: var(--ds-text-subtle);">{t('common.loading')}</div>
      {:else if options.length > 0}
        <div role="listbox" class="max-h-60 overflow-y-auto">
          {#each options as opt, index (opt.value ?? 'unassigned')}
            {@const itemSelected = multiple ? isItemSelected(opt.value) : $isSelected(opt)}
            {@const isHighlighted = highlightedIndex === index}
            <div use:melt={$option(opt)} data-option-value={opt.value ?? ''}
                 onclick={() => selectOption(opt)}
                 onmouseenter={() => highlightedIndex = index}
                 class="px-4 py-3 cursor-pointer border-b last:border-b-0 transition-colors duration-150"
                 style="border-color: var(--ds-border); {itemSelected ? 'background-color: var(--ds-background-selected); color: var(--ds-text);' : isHighlighted ? 'background-color: var(--ds-background-neutral-hovered); color: var(--ds-text);' : 'color: var(--ds-text);'}">
              <div class="flex items-center justify-between">
                <div class="flex items-center gap-3 flex-1 min-w-0">
                  {#if opt.isUnassigned}
                    <span class="font-medium truncate" style="color: var(--ds-text-subtle);">{resolvedUnassignedLabel}</span>
                  {:else if itemSnippet}
                    {@render itemSnippet({ item: opt.item, isSelected: itemSelected })}
                  {:else}
                    {#if iconSnippet}{@render iconSnippet({ item: opt.item })}{/if}
                    <div class="flex flex-col min-w-0">
                      <span class="font-medium truncate">{opt.label}</span>
                    </div>
                  {/if}
                </div>
                {#if itemSelected}<Check class="w-4 h-4 text-blue-600 flex-shrink-0" />{/if}
              </div>
            </div>
          {/each}
          {#if canCreateCurrentInput()}
            <div role="button" tabindex="0"
                 class="px-4 py-3 cursor-pointer border-t hover:bg-gray-50 dark:hover:bg-gray-800 transition-colors duration-150 flex items-center gap-2"
                 style="border-color: var(--ds-border); color: var(--ds-interactive);"
                 onclick={handleCreateOption}
                 onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleCreateOption(); } }}>
              {#if createOptionSnippet}{@render createOptionSnippet({ searchQuery: getCreateQuery(), onCreate })}
              {:else if onCreate}<span class="text-sm">+ {t('pickers.createItem', { value: getCreateQuery() })}</span>{/if}
            </div>
          {/if}
        </div>
      {:else if popoverMode && popoverSearchTerm && !loading}
        <!-- No results (popover mode with search) -->
        <div class="p-4 text-center text-sm" style="color: var(--ds-text-subtle);">
          {#if noResultsSnippet}{@render noResultsSnippet({ searchQuery: popoverSearchTerm })}
          {:else}{t('pickers.noResultsFor', { query: popoverSearchTerm })}{/if}
        </div>
      {:else if !popoverMode}
        <!-- No results (combobox mode) -->
        <div class="p-4 text-center text-sm" style="color: var(--ds-text-subtle);">
          {t('pickers.noItemsFound')}
        </div>
      {/if}

      {#if footer}
        <div class="border-t" style="border-color: var(--ds-border);">{@render footer()}</div>
      {/if}
    </div>
  {/if}

  <!-- Error State (combobox mode) -->
  {#if error && !popoverMode}
    <div class="absolute z-50 w-full mt-2 rounded border shadow-lg"
         style="background-color: var(--ds-surface-raised); border-color: var(--ds-border);">
      <div class="px-4 py-4 text-center text-sm text-red-600">{error}</div>
    </div>
  {/if}
</div>