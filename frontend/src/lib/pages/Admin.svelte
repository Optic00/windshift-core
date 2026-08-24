<script>
  import { onMount, tick } from 'svelte';
  import { currentRoute, navigate } from '../router.js';
  import Input from '../components/Input.svelte';
  import { t } from '../stores/i18n.svelte.js';
  import CustomFields from '../settings/CustomFields.svelte';
  import Workspaces from '../workspaces/Workspaces.svelte';
  import Screens from './Screens.svelte';
  import StatusContainer from '../features/workflows/StatusContainer.svelte';
  import WorkflowBuilder from '../features/workflows/WorkflowBuilder.svelte';
  import ConfigurationSetManager from '../settings/ConfigurationSetManager.svelte';
  import LinkTypeManager from '../settings/LinkTypeManager.svelte';
  import UserManager from '../settings/UserManager.svelte';
  import GroupManager from '../settings/GroupManager.svelte';
  import PermissionsContainer from '../layout/PermissionsContainer.svelte';
  import RoleManager from '../settings/RoleManager.svelte';
  import AttachmentSettings from '../settings/AttachmentSettings.svelte';
  import ModuleSettings from '../settings/ModuleSettings.svelte';
  import HierarchyLevelManager from '../settings/HierarchyLevelManager.svelte';
  import ItemTypeManager from '../settings/ItemTypeManager.svelte';
  import PriorityManager from '../settings/PriorityManager.svelte';
  import NotificationSettings from '../settings/NotificationSettings.svelte';
  import EmailTemplateManager from '../settings/EmailTemplateManager.svelte';
  import ThemeManager from '../settings/ThemeManager.svelte';
  import SSOContainer from '../settings/SSOContainer.svelte';
  import SCMProviderManager from '../settings/SCMProviderManager.svelte';
  import IntegrationsManager from '../settings/IntegrationsManager.svelte';
  import SecuritySettings from '../settings/SecuritySettings.svelte';
  import AIContainer from '../settings/AIContainer.svelte';
  import ActionCapabilitiesManager from '../settings/ActionCapabilitiesManager.svelte';
  import AssetManager from '../features/assets/AssetManager.svelte';
  import Channels from '../features/channels/Channels.svelte';
  import PermissionSetEdit from '../settings/PermissionSetEdit.svelte';
  import ConfigurationSetDetail from '../settings/ConfigurationSetDetail.svelte';
  import ConditionSetManager from '../settings/ConditionSetManager.svelte';
  import ConditionSetDetail from '../settings/ConditionSetDetail.svelte';
  import ApprovalSetManager from '../settings/ApprovalSetManager.svelte';
  import ApprovalSetDetail from '../settings/ApprovalSetDetail.svelte';
  import FormChannelPage from '../features/channels/FormChannelPage.svelte';
  import PortalChannelPage from '../features/channels/PortalChannelPage.svelte';
  import SystemImportPage from '../jira-import/SystemImportPage.svelte';
  import Diagnostics from '../settings/Diagnostics.svelte';
  import { loadExtensions, getExtensionsForPoint } from '../stores/extensions.svelte.js';
  import IframePluginLoader from '../services/IframePluginLoader.svelte';
  import { resolvePluginIcon } from '../utils/pluginIcons.js';
  import PluginModalContainer from '../layout/PluginModalContainer.svelte';
  import LinkComponent from '../components/Link.svelte';
  import SidebarHeader from '../layout/SidebarHeader.svelte';
  import { IconFileText, IconMenu2, IconPuzzle, IconSearch, IconX } from '@tabler/icons-svelte-runes';
  import { useEventListener } from 'runed';
  import PermissionGuard from '../layout/PermissionGuard.svelte';
  import { resolveAdminGroups } from '../admin/adminNavigation.js';
  import { isTypingInField } from '../utils/keyboardShortcuts.js';
  import { isSystemAdmin } from '../stores/permissions.svelte.js';

  const ADMIN_DETAIL_ROUTES = [
    { prefix: '/admin/permission-sets/',    tabId: 'permissions',         component: PermissionSetEdit },
    { prefix: '/admin/configuration-sets/', tabId: 'configuration-sets',  component: ConfigurationSetDetail },
    { prefix: '/admin/condition-sets/',     tabId: 'condition-sets',      component: ConditionSetDetail },
    { prefix: '/admin/approval-sets/',      tabId: 'approval-sets',       component: ApprovalSetDetail },
  ];

  const isFormChannelRoute = $derived(/^\/admin\/channels\/\d+\/forms$/.test($currentRoute.path));
  const isPortalChannelRoute = $derived(/^\/admin\/channels\/\d+\/portal$/.test($currentRoute.path));

  const matchedDetailRoute = $derived(
    ADMIN_DETAIL_ROUTES.find(r => $currentRoute.path.startsWith(r.prefix))
  );
  const isNestedRoute = $derived(!!matchedDetailRoute || isFormChannelRoute || isPortalChannelRoute);

  const activeTab = $derived.by(() => {
    if ($currentRoute.path.startsWith('/admin/channels')) return 'channels';
    if (matchedDetailRoute) return matchedDetailRoute.tabId;
    return $currentRoute.params?.tab || $currentRoute.query?.tab || 'custom-fields';
  });

  // Component-local extensions state — ensures $derived.by tracks changes on reload
  let loadedExtensions = $state({});

  // Search functionality
  let searchQuery = $state('');
  let searchInput = $state(null);
  let adminNavigationOpen = $state(false);
  let adminNavigationToggle = $state(null);

  // Navigation focus management
  let navButtons = $state([]);
  let focusedIndex = $state(-1);

  // Admin navigation groups live in admin/adminNavigation.js
  const adminGroups = $derived(resolveAdminGroups(t));

  // Merge plugin extensions into admin groups
  const adminGroupsWithPlugins = $derived.by(() => {
    const groups = adminGroups.map(g => ({ ...g, items: [...g.items] }));
    const adminTabExtensions = getExtensionsForPoint(loadedExtensions, 'admin.tab');

    // Group extensions by their group property
    const extensionsByGroup = {};
    adminTabExtensions.forEach(ext => {
      const groupName = ext.group || 'Plugins';
      if (!extensionsByGroup[groupName]) {
        extensionsByGroup[groupName] = [];
      }
      extensionsByGroup[groupName].push({
        id: ext.id,
        label: ext.label,
        icon: resolvePluginIcon(ext.icon, IconFileText),
        description: ext.description,
        isPlugin: true,
        pluginData: ext
      });
    });

    // Add or merge extension groups
    Object.entries(extensionsByGroup).forEach(([groupName, items]) => {
      const existingGroup = groups.find(g => g.label === groupName);
      if (existingGroup) {
        // Merge into existing group
        existingGroup.items = [...existingGroup.items, ...items];
      } else {
        // Create new group for plugins
        groups.push({
          id: groupName.toLowerCase().replace(/\s+/g, '-'),
          label: groupName,
          icon: IconPuzzle,
          items
        });
      }
    });

    return groups;
  });

  // Create flat list of all items for search
  const allAdminItems = $derived(adminGroupsWithPlugins.flatMap(group => group.items));
  const activeAdminItem = $derived(allAdminItems.find(item => item.id === activeTab));

  // Filter groups and items based on search
  const filteredGroups = $derived(
    searchQuery.trim() === ''
      ? adminGroupsWithPlugins
      : adminGroupsWithPlugins
          .map(group => ({
            ...group,
            items: group.items.filter(item =>
              item.label.toLowerCase().includes(searchQuery.toLowerCase()) ||
              item.description.toLowerCase().includes(searchQuery.toLowerCase())
            )
          }))
          .filter(group => group.items.length > 0)
  );

  function clearSearch() {
    searchQuery = '';
    searchInput?.focus();
  }

  async function openAdminNavigation() {
    adminNavigationOpen = true;
    await tick();
    searchInput?.focus();
  }

  function closeAdminNavigation(restoreFocus = false) {
    adminNavigationOpen = false;
    if (restoreFocus) {
      void tick().then(() => adminNavigationToggle?.focus());
    }
  }

  // Keyboard navigation for search
  function handleSearchKeydown(event) {
    if (event.key === 'Escape') {
      if (searchQuery) {
        clearSearch();
      } else {
        // If search is already empty, blur the input
        searchInput?.blur();
      }
    } else if (event.key === 'Enter' && filteredGroups.length > 0 && filteredGroups[0].items.length > 0) {
      // Navigate to first item when pressing Enter
      const firstItem = filteredGroups[0].items[0];
      handleTabClick(firstItem.id);
    } else if (event.key === 'ArrowDown') {
      event.preventDefault();
      // Focus first navigation item
      if (navButtons.length > 0) {
        navButtons[0]?.focus();
        focusedIndex = 0;
      }
    }
  }

  // Arrow key navigation for menu items
  function handleNavKeydown(event, currentIndex) {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      const nextIndex = currentIndex + 1;
      if (nextIndex < navButtons.length) {
        navButtons[nextIndex]?.focus();
        focusedIndex = nextIndex;
      }
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      const prevIndex = currentIndex - 1;
      if (prevIndex >= 0) {
        navButtons[prevIndex]?.focus();
        focusedIndex = prevIndex;
      } else {
        // Go back to search
        searchInput?.focus();
        focusedIndex = -1;
      }
    } else if (event.key === 'Home') {
      event.preventDefault();
      if (navButtons.length > 0) {
        navButtons[0]?.focus();
        focusedIndex = 0;
      }
    } else if (event.key === 'End') {
      event.preventDefault();
      const lastIndex = navButtons.length - 1;
      if (lastIndex >= 0) {
        navButtons[lastIndex]?.focus();
        focusedIndex = lastIndex;
      }
    }
  }

  // Global keyboard shortcut handler
  function handleGlobalKeydown(event) {
    if (event.key === 'Escape' && adminNavigationOpen) {
      event.preventDefault();
      closeAdminNavigation(true);
      return;
    }

    // Focus search on '/' key, but never steal printable input from forms/editors
    // or modal dialogs (e.g. the global create dialog opened over admin pages).
    if (event.key !== '/' || isTypingInField(event) || document.querySelector('[role="dialog"][aria-modal="true"]')) return;

    event.preventDefault();
    if (window.matchMedia('(max-width: 1100px)').matches) {
      void openAdminNavigation();
    } else {
      searchInput?.focus();
    }
  }

  function handleTabClick(tabId) {
    navigate(`/admin/${tabId}`);
  }

  onMount(async () => {
    // Load plugin extensions into component-local state for proper reactivity
    loadedExtensions = (await loadExtensions()) || {};

    if (!$currentRoute.params?.tab && !isNestedRoute && !$currentRoute.path.startsWith('/admin/channels')) {
      navigate('/admin/custom-fields');
    }
  });

  useEventListener(() => window, 'admin-tab-switch', (/** @type {CustomEvent<{tab?: string}>} */ event) => {
    if (!event.detail?.tab) return;
    if (event.detail.tab === 'action-credentials') {
      navigate('/admin/action-capabilities?tab=credentials');
      return;
    }
    navigate(`/admin/${event.detail.tab}`);
  });
  useEventListener(() => window, 'keydown', handleGlobalKeydown);

  $effect(() => {
    if ($currentRoute.path === '/admin/action-credentials') {
      navigate('/admin/action-capabilities?tab=credentials', { replace: true });
    }
  });

  // Calculate button indices for arrow navigation
  const buttonIndices = $derived.by(() => {
    const indices = new Map();
    let globalIndex = 0;
    filteredGroups.forEach(group => {
      group.items.forEach(item => {
        indices.set(item.id, globalIndex);
        globalIndex++;
      });
    });
    return indices;
  });

  // Total button count for validation
  const totalButtons = $derived(filteredGroups.reduce((count, group) => count + group.items.length, 0));

  function switchTab(tab) {
    navigate(`/admin/${tab}`);
  }
</script>

<!-- Main container with sidebar layout -->
<div class="admin-shell flex min-h-screen min-w-0" style="background-color: var(--ds-surface);">
  <!-- Channel managers can use channel routes without seeing the rest of the
       system-administration navigation. Non-channel admin routes remain
       guarded in MainApp. -->
  {#if $isSystemAdmin}
    {#if adminNavigationOpen}
      <button
        type="button"
        class="admin-navigation-backdrop"
        data-testid="admin-navigation-backdrop"
        aria-label="Close admin navigation"
        onclick={() => closeAdminNavigation(true)}
      ></button>
    {/if}
    <aside
      id="admin-navigation"
      class:admin-navigation-open={adminNavigationOpen}
      class="admin-sidebar w-64 border-r flex-shrink-0"
      style="border-color: var(--ds-border); background-color: var(--ds-surface-raised);"
      aria-label="Admin settings"
    >
    <div class="p-6">
      <div class="admin-sidebar-heading">
        <div class="admin-sidebar-title">
          <SidebarHeader title={t('settings.admin')} description={t('settings.systemSettings')} noBorder />
        </div>
        <button
          type="button"
          class="admin-navigation-close"
          data-testid="admin-navigation-close"
          aria-label="Close admin navigation"
          onclick={() => closeAdminNavigation(true)}
        >
          <IconX size={20} stroke={1.5} aria-hidden="true" />
        </button>
      </div>
      
      <!-- Search -->
      <div class="mb-4 relative">
        <label for="admin-search" class="sr-only">Search admin settings</label>
        <div class="relative">
          <IconSearch size={16} stroke={1.5} class="absolute left-3 top-1/2 transform -translate-y-1/2" style="color: var(--ds-icon-subtle);" aria-hidden="true" />
          <Input
            id="admin-search"
            bind:this={searchInput}
            bind:value={searchQuery}
            onkeydown={handleSearchKeydown}
            type="search"
            placeholder={t('common.search')}
            class="pl-10 pr-8"
            ariaDescribedby={searchQuery && filteredGroups.length === 0 ? 'search-no-results' : undefined}
            size="small"
          />
          {#if searchQuery}
            <button
              onclick={clearSearch}
              class="absolute right-2 top-1/2 transform -translate-y-1/2 p-1 transition-colors"
              style="color: var(--ds-icon-subtle);"
              onmouseenter={(e) => e.currentTarget.style.color = 'var(--ds-icon)'}
              onmouseleave={(e) => e.currentTarget.style.color = 'var(--ds-icon-subtle)'}
              aria-label={t('search.clearSearch')}
            >
              <IconX size={12} stroke={1.5} aria-hidden="true" />
            </button>
          {/if}
        </div>
      </div>

      <!-- Navigation -->
      <nav class="space-y-6 pb-6" aria-label="Admin settings">
        {#each filteredGroups as group}
          <div role="group" aria-labelledby="group-{group.id}">
            <!-- Group Header -->
            <div class="px-2 pt-3 pb-1 mb-1">
              <h3 id="group-{group.id}" class="text-xs font-semibold uppercase tracking-wider" style="color: var(--ds-text-subtle);">
                {group.label}
              </h3>
            </div>

            <!-- Group Items -->
            <div class="space-y-1">
              {#each group.items as item}
                {@const buttonIndex = buttonIndices.get(item.id)}
                {@const isItemActive = activeTab === item.id}
                <LinkComponent
                  data-testid="admin-navigation-item"
                  id="admin-navigation-item-{item.id}"
                  bind:element={navButtons[buttonIndex]}
                  href="/admin/{item.id}"
                  active={isItemActive}
                  onClick={() => closeAdminNavigation()}
                  onkeydown={(e) => handleNavKeydown(e, buttonIndex)}
                  class="w-full group flex items-center px-3 py-2 text-sm font-medium rounded-lg transition-all cursor-pointer"
                  style={isItemActive ? 'background: var(--ds-surface-selected); color: var(--ds-text);' : 'color: var(--ds-text-subtle);'}
                  onmouseenter={(e) => { if (!isItemActive) e.currentTarget.style.cssText = 'background: var(--ds-background-neutral-hovered); color: var(--ds-text);'; }}
                  onmouseleave={(e) => { if (!isItemActive) e.currentTarget.style.cssText = 'color: var(--ds-text-subtle);'; }}
                >
                  {@const ItemIcon = item.icon}
                  <ItemIcon size={16} stroke={1.5} class="flex-shrink-0 -ml-1 mr-3" aria-hidden="true" />
                  <span>{item.label}</span>
                </LinkComponent>
              {/each}
            </div>
          </div>
        {/each}
        
        {#if filteredGroups.length === 0 && searchQuery}
          <div class="text-center py-4" role="status" id="search-no-results">
            <p class="text-sm" style="color: var(--ds-text-subtle);">{t('search.noSearchResults')}</p>
            <button
              onclick={clearSearch}
              class="text-xs mt-1"
              style="color: var(--ds-link);"
              onmouseenter={(e) => e.currentTarget.style.color = 'var(--ds-link-pressed)'}
              onmouseleave={(e) => e.currentTarget.style.color = 'var(--ds-link)'}
            >
              {t('search.clearSearch')}
            </button>
          </div>
        {/if}

        <!-- Live region for search results announcements -->
        <div class="sr-only" role="status" aria-live="polite" aria-atomic="true">
          {#if searchQuery && filteredGroups.length > 0}
            {filteredGroups.reduce((count, group) => count + group.items.length, 0)} result{filteredGroups.reduce((count, group) => count + group.items.length, 0) === 1 ? '' : 's'} found
          {:else if searchQuery && filteredGroups.length === 0}
            No results found
          {/if}
        </div>
      </nav>
    </div>
    </aside>
  {/if}

  <!-- Main Content -->
  <div class="admin-main flex-1 flex flex-col min-w-0 overflow-hidden">
    {#if $isSystemAdmin}
      <header class="admin-mobile-header">
        <button
          type="button"
          class="admin-navigation-toggle"
          data-testid="admin-navigation-toggle"
          bind:this={adminNavigationToggle}
          aria-label="Open admin navigation"
          aria-controls="admin-navigation"
          aria-expanded={adminNavigationOpen}
          onclick={openAdminNavigation}
        >
          <IconMenu2 size={22} stroke={1.5} aria-hidden="true" />
        </button>
        <div class="min-w-0">
          <p class="admin-mobile-eyebrow">{t('settings.admin')}</p>
          <p class="admin-mobile-section" data-testid="admin-active-section">
            {activeAdminItem?.label || t('settings.systemSettings')}
          </p>
        </div>
      </header>
    {/if}
    <!-- Nested detail routes (no padding) -->
    {#if isFormChannelRoute}
      <FormChannelPage />
    {:else if isPortalChannelRoute}
      <PortalChannelPage />
    {:else if matchedDetailRoute}
      {@const DetailComponent = matchedDetailRoute.component}
      <DetailComponent />
    {:else}
    <div class="admin-content px-16 py-12 pb-0 flex-1 min-w-0 overflow-y-auto">
      <div class="min-w-0 pr-0 pl-0">
      <!-- Custom Fields Tab -->
  {#if activeTab === 'custom-fields'}
    <CustomFields />
  {/if}

  <!-- Workspaces Tab -->
  {#if activeTab === 'workspaces'}
    <Workspaces noPadding />
  {/if}

  <!-- Screens Tab -->
  {#if activeTab === 'screens'}
    <Screens />
  {/if}

  <!-- Statuses (Categories & Individual) Tab -->
  {#if activeTab === 'statuses'}
    <StatusContainer />
  {/if}

  <!-- Workflows Tab -->
  {#if activeTab === 'workflows'}
    <WorkflowBuilder />
  {/if}

  <!-- Configuration Sets Tab -->
  {#if activeTab === 'configuration-sets'}
    <ConfigurationSetManager />
  {/if}

  <!-- Condition Sets Tab -->
  {#if activeTab === 'condition-sets'}
    <ConditionSetManager />
  {/if}

  <!-- Approval Sets Tab -->
  {#if activeTab === 'approval-sets'}
    <ApprovalSetManager />
  {/if}

  <!-- Notification Settings Tab -->
  {#if activeTab === 'notification-settings'}
    <NotificationSettings />
  {/if}

  <!-- Email Templates Tab -->
  {#if activeTab === 'email-templates'}
    <EmailTemplateManager />
  {/if}

  <!-- Channels Tab -->
  {#if activeTab === 'channels'}
    <Channels embedded={true} />
  {/if}

  <!-- Link Types Tab -->
  {#if activeTab === 'link-types'}
    <LinkTypeManager />
  {/if}

  <!-- User Management Tab -->
  {#if activeTab === 'users'}
    <UserManager />
  {/if}

  <!-- Group Management Tab -->
  {#if activeTab === 'groups'}
    <GroupManager />
  {/if}

  <!-- Permissions & Permission Sets Tab -->
  {#if activeTab === 'permissions'}
    <PermissionsContainer />
  {/if}

  <!-- Workspace Roles Tab -->
  {#if activeTab === 'workspace-roles'}
    <RoleManager />
  {/if}

  <!-- Attachment Settings Tab -->
  {#if activeTab === 'attachments'}
    <AttachmentSettings />
  {/if}

  <!-- Module Settings Tab -->
  {#if activeTab === 'modules'}
    <ModuleSettings />
  {/if}

  <!-- Theme Settings Tab -->
  {#if activeTab === 'themes'}
    <ThemeManager />
  {/if}

  <!-- Hierarchy Levels Tab -->
  {#if activeTab === 'hierarchy-levels'}
    <HierarchyLevelManager />
  {/if}

  <!-- Item Types Tab -->
  {#if activeTab === 'item-types'}
    <ItemTypeManager />
  {/if}

  <!-- Priorities Tab -->
  {#if activeTab === 'priorities'}
    <PriorityManager />
  {/if}

  <!-- SSO Settings Tab -->
  {#if activeTab === 'sso'}
    <SSOContainer extensions={loadedExtensions} />
  {/if}

  <!-- SCM Providers Tab -->
  {#if activeTab === 'scm-providers'}
    <SCMProviderManager />
  {/if}

  {#if activeTab === 'integration-providers'}
    <IntegrationsManager />
  {/if}

  <!-- AI Connections Tab (with sub-tabs for Connections + Features + Agent Templates) -->
  {#if activeTab === 'llm-connections'}
    <AIContainer />
  {/if}

  <!-- Action Capabilities Tab -->
  {#if activeTab === 'action-capabilities'}
    <ActionCapabilitiesManager />
  {/if}

  <!-- System Import Tab -->
  {#if activeTab === 'system-import'}
    <SystemImportPage />
  {/if}

  <!-- Security Settings Tab -->
  {#if activeTab === 'security'}
    <SecuritySettings />
  {/if}

  <!-- Asset Management Tab -->
  {#if activeTab === 'assets'}
    <AssetManager />
  {/if}

  <!-- System Diagnostics Tab -->
  {#if activeTab === 'diagnostics'}
    <Diagnostics />
  {/if}

  <!-- Plugin Components -->
  {#each allAdminItems.filter(item => item.isPlugin) as pluginItem}
    {#if activeTab === pluginItem.id}
      {@const pluginName = pluginItem.pluginData?.pluginName || 'unknown'}
      {@const iframeSrc = `/api/plugins/${pluginName}/assets/${pluginItem.pluginData?.component || 'index.html'}`}

      <div class="plugin-component-container">
        <!-- All plugins use iframe mode -->
        <IframePluginLoader
          pluginName={pluginItem.label}
          src={iframeSrc}
        />
      </div>
    {/if}
  {/each}
      </div>
    </div>
    {/if}
  </div>
</div>

<!-- Plugin Modal Container - renders modals requested by iframe plugins -->
<PluginModalContainer />

<style>
  .admin-shell {
    position: relative;
    width: 100%;
  }

  .admin-mobile-header,
  .admin-navigation-close,
  .admin-navigation-backdrop {
    display: none;
  }

  @media (max-width: 1100px) {
    .admin-sidebar {
      position: absolute;
      z-index: 30;
      inset: 0 auto 0 0;
      width: min(20rem, calc(100% - 3rem));
      overflow-y: auto;
      visibility: hidden;
      transform: translateX(-100%);
      transition:
        transform var(--duration-normal, 200ms) var(--ease-smooth, ease),
        visibility var(--duration-normal, 200ms) var(--ease-smooth, ease);
      box-shadow: var(--ds-shadow-overlay, 0 8px 24px rgb(9 30 66 / 25%));
    }

    .admin-sidebar.admin-navigation-open {
      visibility: visible;
      transform: translateX(0);
    }

    .admin-sidebar-heading {
      display: flex;
      align-items: flex-start;
      gap: 0.75rem;
    }

    .admin-sidebar-title {
      min-width: 0;
      flex: 1;
    }

    .admin-navigation-close,
    .admin-navigation-toggle {
      display: inline-flex;
      width: 2.75rem;
      height: 2.75rem;
      flex: 0 0 2.75rem;
      align-items: center;
      justify-content: center;
      border: 0;
      border-radius: 0.5rem;
      color: var(--ds-icon);
      background: transparent;
    }

    .admin-navigation-close:hover,
    .admin-navigation-toggle:hover {
      background: var(--ds-background-neutral-hovered);
    }

    .admin-navigation-close:focus-visible,
    .admin-navigation-toggle:focus-visible {
      outline: 2px solid var(--ds-border-focused, var(--ds-link));
      outline-offset: 2px;
    }

    .admin-navigation-backdrop {
      display: block;
      position: absolute;
      z-index: 20;
      inset: 0;
      border: 0;
      background: color-mix(in srgb, var(--ds-blanket, #091e42) 54%, transparent);
    }

    .admin-mobile-header {
      display: flex;
      position: sticky;
      z-index: 10;
      top: 0;
      min-width: 0;
      align-items: center;
      gap: 0.75rem;
      padding: 0.625rem 1rem;
      border-bottom: 1px solid var(--ds-border);
      background: var(--ds-surface-raised);
    }

    .admin-mobile-eyebrow {
      color: var(--ds-text-subtle);
      font-size: 0.6875rem;
      font-weight: 600;
      line-height: 1rem;
      text-transform: uppercase;
      letter-spacing: 0.04em;
    }

    .admin-mobile-section {
      overflow: hidden;
      color: var(--ds-text);
      font-size: 0.875rem;
      font-weight: 600;
      line-height: 1.25rem;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .admin-content {
      padding: 1.5rem clamp(1rem, 4vw, 2rem) 0;
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .admin-sidebar {
      transition: none;
    }
  }

  :global(.tiptap-editor) {
    outline: none;
    white-space: pre-wrap;
  }

  :global(.tiptap-editor .ProseMirror) {
    outline: none;
    padding: 1rem;
    min-height: 350px;
  }

  :global(.tiptap-editor h1) {
    font-size: 1.875rem;
    font-weight: 600;
    margin: 1.5rem 0 1rem 0;
    color: var(--ds-text);
    line-height: 1.2;
  }

  :global(.tiptap-editor h2) {
    font-size: 1.5rem;
    font-weight: 600;
    margin: 1.25rem 0 0.75rem 0;
    color: var(--ds-text);
    line-height: 1.3;
  }

  :global(.tiptap-editor h3) {
    font-size: 1.25rem;
    font-weight: 600;
    margin: 1rem 0 0.5rem 0;
    color: var(--ds-text);
    line-height: 1.4;
  }

  :global(.tiptap-editor p) {
    margin: 0.75rem 0;
    line-height: 1.6;
  }

  :global(.tiptap-editor p:first-child) {
    margin-top: 0;
  }

  :global(.tiptap-editor p:last-child) {
    margin-bottom: 0;
  }

  :global(.tiptap-editor ul, .tiptap-editor ol) {
    padding-left: 1.5rem;
    margin: 0.75rem 0;
  }

  :global(.tiptap-editor li) {
    margin: 0.25rem 0;
    line-height: 1.5;
  }

  :global(.tiptap-editor strong) {
    font-weight: 600;
  }

  :global(.tiptap-editor em) {
    font-style: italic;
  }

  :global(.tiptap-editor code) {
    background: var(--ds-background-neutral);
    padding: 2px 4px;
    border-radius: 3px;
    font-family: monospace;
    font-size: 0.875rem;
    color: var(--ds-text);
  }

  :global(.tiptap-editor hr) {
    border: none;
    border-top: 2px solid var(--ds-border);
    margin: 1.5rem 0;
  }

  :global(.tiptap-editor blockquote) {
    border-left: 1px solid var(--ds-border);
    padding-left: 1rem;
    margin: 1rem 0;
    font-style: italic;
  }

  /* Placeholder styling */
  :global(.tiptap-editor .ProseMirror p.is-editor-empty:first-child::before) {
    content: attr(data-placeholder);
    float: left;
    color: var(--ds-text-subtlest);
    pointer-events: none;
    height: 0;
  }

  /* Ensure proper spacing and line breaks */
  :global(.tiptap-editor br) {
    display: block;
    content: "";
    margin-top: 0.5rem;
  }
</style>
