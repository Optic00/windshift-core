<script>
  import { onMount } from 'svelte';
  import { slide } from 'svelte/transition';
  import { currentRoute, navigate, isWorkspaceRoute, GLOBAL_COLLECTION_VIEWS } from '../router.js';
  import { authStore, permissionStore, uiStore, currentWorkspace, workspacesStore, workspacePermissions, ssoStore, workspaceDataStore, activityStore, collectionStore, homepageStore } from '../stores';
  import EmailVerificationBanner from '../features/notifications/EmailVerificationBanner.svelte';
  import { moduleSettings } from '../stores/moduleSettings.js';
  import { attachmentStatus } from '../stores/attachmentStatus.svelte.js';
  import { aiStore } from '../stores/aiStore.svelte.js';
  import { chatStore } from '../stores/chatStore.svelte.js';
  import { logbookStore } from '../stores/logbook.svelte.js';
  import { capabilitiesStore } from '../stores/capabilities.svelte.js';
  import { startNotificationPoller, stopNotificationPoller } from '../stores/notifications.js';
  import { resetAuthenticatedShellState, runAuthenticatedShellBootstrap } from '../services/authenticatedShellBootstrap.js';
  import { desktopBridge } from '../desktop/bridge.svelte.js';
  import { initDesktopFocusRefresh } from '../utils/desktopFocusRefresh.svelte.js';
  import { api } from '../api.js';
  import { t } from '../stores/i18n.svelte.js';
  import NotFound from './NotFound.svelte';
  import Workspaces from '../workspaces/Workspaces.svelte';
  import WorkspaceSettings from '../workspaces/WorkspaceSettings.svelte';
  import Collections from '../features/collections/Collections.svelte';
  import CollectionsList from '../features/collections/CollectionsList.svelte';
  import NotificationsPage from './NotificationsPage.svelte';
  import ApprovalsInbox from './ApprovalsInbox.svelte';
  import UserProfile from './UserProfile.svelte';
  import Security from './Security.svelte';
  import SearchPage from './SearchPage.svelte';
  import About from './About.svelte';
  import ApiDocs from './ApiDocs.svelte';
  import CliAuthorize from './CliAuthorize.svelte';
  import OAuthAuthorize from './OAuthAuthorize.svelte';
  import Channels from '../features/channels/Channels.svelte';
  import Customers from '../workspaces/Customers.svelte';
  import TeamsList from '../teams/TeamsList.svelte';
  import TeamDetail from '../teams/TeamDetail.svelte';
  import Hub from '../layout/Hub.svelte';
  import Footer from '../layout/Footer.svelte';
  import {
    Layers3, BarChart3, Sheet, Target, User, Notebook, GitBranch, MapPin, Shield, Home, CheckSquare, MoreHorizontal, Inbox, SquareKanban, FolderOpen
  } from '@lucide/svelte';
  import GlobalConfirmDialog from '../dialogs/GlobalConfirmDialog.svelte';
  import FloatingTimer from '../features/time/FloatingTimer.svelte';
  import ToastContainer from '../features/notifications/ToastContainer.svelte';
  import Spinner from '../components/Spinner.svelte';
  import ModalBackdrop from '../components/ModalBackdrop.svelte';
  import Button from '../components/Button.svelte';
  import PermissionGuard from '../layout/PermissionGuard.svelte';
  import UnauthorizedAccess from './UnauthorizedAccess.svelte';
  import WorkspaceNavigation from '../workspaces/WorkspaceNavigation.svelte';
  import CollectionNavigation from '../features/collections/CollectionNavigation.svelte';
  import { clearWorkspaceGradient } from '../stores/workspaceGradient.svelte.js';
  import { useEventListener } from 'runed';
  import { toHotkeyString } from '../utils/keyboardShortcuts.js';
  import { LazyComponentLoader } from '../utils/lazyComponentLoader.svelte.js';
  import { hasSessionExpired } from '../utils/lazyLoadRecovery.js';
  import MainSidebar from '../layout/MainSidebar.svelte';
  import { terminalStore } from '../stores/terminalStore.svelte.js';

  let showCommandPalette = $state(false);
  let showCreateModal = $state(false);
  let showChatPanel = $state(false);
  let createModalInitialType = $state('work-item');
  let createModalSkipNavigate = $state(false);
  let createModalWorkspaceId = $state(null);
  let showEmailVerificationBanner = $state(false);

  // Terminal panel state
  let terminalState = $derived($terminalStore);
  let TerminalPanelComponent = $state(null);
  let terminalLoading = $state(false);
  let isResizingTerminal = $state(false);
  let resizeStartX = $state(0);
  let resizeStartPercent = $state(50);
  let PomodoroSettingsModalComponent = $state(null);
  let AboutModalComponent = $state(null);
  let desktopModalLoading = $state(false);

  async function loadTerminalPanel() {
    if (TerminalPanelComponent || terminalLoading) return;
    terminalLoading = true;
    try {
      const module = await import('../features/terminal/TerminalPanel.svelte');
      TerminalPanelComponent = module.default;
    } catch (err) {
      console.error('Failed to load terminal panel:', err);
    } finally {
      terminalLoading = false;
    }
  }

  function toggleTerminal() {
    terminalStore.toggle();
    if (!TerminalPanelComponent) {
      loadTerminalPanel();
    }
  }

  async function loadDesktopModal(modal) {
    if (desktopModalLoading) return;
    if (modal === 'pomodoro-settings' && PomodoroSettingsModalComponent) return;
    if (modal === 'about' && AboutModalComponent) return;

    desktopModalLoading = true;
    try {
      if (modal === 'pomodoro-settings') {
        const module = await import('../dialogs/PomodoroSettingsModal.svelte');
        PomodoroSettingsModalComponent = module.default;
      } else if (modal === 'about') {
        const module = await import('../dialogs/AboutModal.svelte');
        AboutModalComponent = module.default;
      }
    } catch (err) {
      console.error('Failed to load desktop modal:', err);
    } finally {
      desktopModalLoading = false;
    }
  }

  function handleTerminalResizeStart(e) {
    e.preventDefault();
    resizeStartX = e.clientX;
    resizeStartPercent = terminalState.splitPercent;
    isResizingTerminal = true;
  }

  function onTerminalMouseMove(e) {
    const container = document.querySelector('.main-split-container');
    if (!container) return;
    const rect = container.getBoundingClientRect();
    const deltaX = e.clientX - resizeStartX;
    const deltaPercent = (deltaX / rect.width) * 100;
    const newPercent = resizeStartPercent + deltaPercent;
    terminalStore.setSplitPercent(newPercent);
  }

  function onTerminalMouseUp() {
    isResizingTerminal = false;
  }

  useEventListener(() => (isResizingTerminal ? document : null), 'mousemove', onTerminalMouseMove);
  useEventListener(() => (isResizingTerminal ? document : null), 'mouseup', onTerminalMouseUp);

  $effect(() => {
    if (!isResizingTerminal) return;
    document.body.style.cursor = 'col-resize';
    document.body.style.userSelect = 'none';
    return () => {
      document.body.style.cursor = '';
      document.body.style.userSelect = '';
    };
  });

  let lastSpaceTime = 0;
  let sessionRevalidationPromise = null;
  const DOUBLE_SPACE_THRESHOLD = 300; // milliseconds

  // Security and other late-mounted consumers can await the shell feature
  // snapshot instead of racing it with a second /api/features request.
  capabilitiesStore.beginHydration();

  // Component loaders with literal import paths for Vite's static analysis
  const componentLoaders = {
    'admin': () => import('./Admin.svelte'),
    'time': () => import('../features/time/Time.svelte'),
    'test-cases': () => import('../features/testing/TestCases.svelte'),
    'test-sets': () => import('../features/testing/TestSets.svelte'),
    'test-templates': () => import('../features/testing/TestTemplates.svelte'),
    'test-runs': () => import('../features/testing/TestRuns.svelte'),
    'test-reports': () => import('../features/testing/TestReports.svelte'),
    'test-steps': () => import('../features/testing/TestSteps.svelte'),
    'test-execution': () => import('../features/testing/TestExecution.svelte'),
    'test-run-detail': () => import('../features/testing/TestRunDetail.svelte'),
    'test-template-detail': () => import('../features/testing/TestTemplateDetail.svelte'),
    'milestones': () => import('../features/milestones/Milestones.svelte'),
    'milestone-detail': () => import('../features/milestones/MilestoneDetail.svelte'),
    'iterations': () => import('../features/iterations/Iterations.svelte'),
    'iteration-detail': () => import('../features/iterations/IterationDetail.svelte'),
    'iteration-dependencies': () => import('../features/iterations/IterationDependencies.svelte'),
    'assets': () => import('../features/assets/AssetBrowser.svelte'),
    'asset-detail': () => import('../features/assets/AssetBrowser.svelte'),
    'asset-settings': () => import('../features/assets/AssetManager.svelte'),
    'workspace-board': () => import('../features/collections/CollectionBoard.svelte'),
    'workspace-board-config': () => import('../settings/BoardConfigurationPage.svelte'),
    'workspace-backlog': () => import('../features/collections/CollectionBacklog.svelte'),
    'workspace-list': () => import('../features/collections/CollectionList.svelte'),
    'workspace-tree': () => import('../features/collections/CollectionTree.svelte'),
    'workspace-map': () => import('../features/collections/CollectionMap.svelte'),
    'workspace-roadmap': () => import('../features/collections/CollectionRoadmap.svelte'),
    'workspace-pages': () => import('../features/pages/PagesView.svelte'),
    'workspace-pages-archived': () => import('../features/pages/ArchivedPagesPage.svelte'),
    // Global collection views (no workspace)
    'collection-board': () => import('../features/collections/CollectionBoard.svelte'),
    'collection-board-config': () => import('../settings/BoardConfigurationPage.svelte'),
    'collection-backlog': () => import('../features/collections/CollectionBacklog.svelte'),
    'collection-list': () => import('../features/collections/CollectionList.svelte'),
    'collection-tree': () => import('../features/collections/CollectionTree.svelte'),
    'collection-map': () => import('../features/collections/CollectionMap.svelte'),
    'collection-roadmap': () => import('../features/collections/CollectionRoadmap.svelte'),
    'workspace-iterations': () => import('../features/iterations/Iterations.svelte'),
    'workspace-milestones': () => import('../features/milestones/Milestones.svelte'),
    'workspace-actions': () => import('../features/actions/ActionsSettings.svelte'),
    'workspace-analytics': () => import('../features/analytics/WorkspaceAnalytics.svelte'),
    'command-palette': () => import('../layout/CommandPalette.svelte'),
    'create-modal': () => import('../dialogs/CreateModal.svelte'),
    'homepage': () => import('./Homepage.svelte'),
    'licenses': () => import('./Licenses.svelte'),
    'item-detail': () => import('../features/items/ItemDetail.svelte'),
    'personal-task-detail': () => import('../features/personal/PersonalTaskDetail.svelte'),
    'workspace-detail': () => import('../workspaces/WorkspaceWelcome.svelte'),
    'workspace-overview': () => import('../workspaces/WorkspaceWelcome.svelte'),
    'personal-workspace': () => import('../workspaces/WorkspaceDetail.svelte'),
    'workspace-calendar': () => import('../features/time/WeeklyCalendar.svelte'),
    'workspace-reviews': () => import('../features/personal/PersonalReview.svelte'),
    'workflow-designer': () => import('../features/workflows/WorkflowDesigner.svelte'),
    'configuration-set-detail': () => import('../settings/ConfigurationSetDetail.svelte'),
    'workspace-look-and-feel': () => import('../workspaces/WorkspaceLookAndFeel.svelte'),
    'personal-plan': () => import('../features/personal/PlanMyDay.svelte'),
    'logbook': () => import('../features/logbook/Logbook.svelte'),
    'logbook-document': () => import('../features/logbook/DocumentDetail.svelte'),
    'chat-panel': () => import('../features/chat/ChatPanel.svelte'),
    'terminal-panel': () => import('../features/terminal/TerminalPanel.svelte')
  };

  const lazyComponents = new LazyComponentLoader(componentLoaders, {
    onError: (view, error) => {
      console.error(`Failed to load component for ${view}:`, error);
      void recoverFromLazyLoadFailure();
    },
  });

  async function recoverFromLazyLoadFailure() {
    if (!authStore.isAuthenticated) return;

    sessionRevalidationPromise ??= hasSessionExpired(api.auth.getCurrentUser);
    const sessionExpired = await sessionRevalidationPromise;
    sessionRevalidationPromise = null;

    if (sessionExpired) {
      // fetchAPI also clears auth on a 401. Keep this explicit so the recovery
      // contract remains correct if the session check implementation changes.
      authStore.clearAuth();
      showCommandPalette = false;
      closeCreateModal();
      showChatPanel = false;
    }
  }

  function closeCreateModal() {
    showCreateModal = false;
    createModalInitialType = 'work-item';
    createModalSkipNavigate = false;
    createModalWorkspaceId = null;
  }

  // Route configuration for lazy-loaded components (metadata only)
  const routeConfig = {
    'admin': {
      loadingMsg: 'Loading Admin Panel...',
      errorMsg: 'Failed to load Admin Panel',
      requirePermission: 'systemAdmin'
    },
    'time': {
      loadingMsg: 'Loading Time & Projects...',
      errorMsg: 'Failed to load Time & Projects'
    },
    'test-cases': {
      loadingMsg: 'Loading Test Cases...',
      errorMsg: 'Failed to load Test Cases',
      wrapper: 'none',
      getProps: (route) => ({ workspaceId: route.params.id })
    },
    'test-sets': {
      loadingMsg: 'Loading Test Plans...',
      errorMsg: 'Failed to load Test Plans',
      wrapper: 'none',
      getProps: (route) => ({ workspaceId: route.params.id })
    },
    'test-templates': {
      loadingMsg: 'Loading Test Templates...',
      errorMsg: 'Failed to load Test Templates',
      wrapper: 'none',
      getProps: (route) => ({ workspaceId: route.params.id })
    },
    'test-runs': {
      loadingMsg: 'Loading Test Runs...',
      errorMsg: 'Failed to load Test Runs',
      wrapper: 'none',
      getProps: (route) => ({ workspaceId: route.params.id })
    },
    'test-reports': {
      loadingMsg: 'Loading Test Reports...',
      errorMsg: 'Failed to load Test Reports',
      wrapper: 'none',
      getProps: (route) => ({ workspaceId: route.params.id })
    },
    'test-steps': {
      loadingMsg: 'Loading Test Steps...',
      errorMsg: 'Failed to load Test Steps',
      wrapper: 'none',
      getProps: (route) => ({ workspaceId: route.params.id })
    },
    'test-execution': {
      loadingMsg: 'Loading Test Execution...',
      errorMsg: 'Failed to load Test Execution',
      wrapper: 'none'
    },
    'test-run-detail': {
      loadingMsg: 'Loading Test Run Details...',
      errorMsg: 'Failed to load Test Run Details',
      wrapper: 'none'
    },
    'test-template-detail': {
      loadingMsg: 'Loading Template Details...',
      errorMsg: 'Failed to load Template Details',
      wrapper: 'none'
    },
    'milestones': {
      loadingMsg: 'Loading Milestones...',
      errorMsg: 'Failed to load Milestones',
      wrapper: 'surface-full'
    },
    'milestone-detail': {
      loadingMsg: 'Loading Milestone...',
      errorMsg: 'Failed to load Milestone',
      wrapper: 'surface-full',
      getProps: (route) => ({
        milestoneId: route.params.id,
        workspaceId: route.query?.workspaceId || null
      })
    },
    'iterations': {
      loadingMsg: 'Loading Iterations...',
      errorMsg: 'Failed to load Iterations',
      wrapper: 'surface-full',
      getProps: (route) => ({ typeId: route.params.typeId })
    },
    'iteration-detail': {
      loadingMsg: 'Loading Iteration...',
      errorMsg: 'Failed to load Iteration',
      wrapper: 'surface-full',
      getProps: (route) => ({
        iterationId: route.params.id,
        workspaceId: route.query?.workspaceId || null
      })
    },
    'iteration-dependencies': {
      loadingMsg: 'Loading Dependency Analysis...',
      errorMsg: 'Failed to load Dependency Analysis',
      wrapper: 'surface-full',
      getProps: (route) => ({
        iterationId: route.params.id,
      })
    },
    'assets': {
      loadingMsg: 'Loading Assets...',
      errorMsg: 'Failed to load Assets',
      wrapper: 'surface-full'
    },
    'asset-detail': {
      loadingMsg: 'Loading Asset...',
      errorMsg: 'Failed to load Asset',
      wrapper: 'surface-full',
      getProps: (route) => ({ assetId: route.params.id })
    },
    'asset-settings': {
      loadingMsg: 'Loading Asset Settings...',
      errorMsg: 'Failed to load Asset Settings',
      wrapper: 'surface-admin'
    },
    'workspace-board': {
      loadingMsg: 'Loading Board View...',
      errorMsg: 'Failed to load Board View',
      getProps: (route) => ({ workspaceId: route.params.id, collectionId: route.params.collectionId })
    },
    'workspace-board-config': {
      loadingMsg: 'Loading Board Configuration...',
      errorMsg: 'Failed to load Board Configuration',
      getProps: (route) => ({ workspaceId: route.params.id, collectionId: route.params.collectionId })
    },
    'workspace-backlog': {
      loadingMsg: 'Loading Backlog View...',
      errorMsg: 'Failed to load Backlog View',
      getProps: (route) => ({ workspaceId: route.params.id, collectionId: route.params.collectionId })
    },
    'workspace-list': {
      loadingMsg: 'Loading List View...',
      errorMsg: 'Failed to load List View',
      getProps: (route) => ({ workspaceId: route.params.id, collectionId: route.params.collectionId })
    },
    'workspace-tree': {
      loadingMsg: 'Loading Tree View...',
      errorMsg: 'Failed to load Tree View',
      getProps: (route) => ({ workspaceId: route.params.id, collectionId: route.params.collectionId })
    },
    'workspace-map': {
      loadingMsg: 'Loading Map View...',
      errorMsg: 'Failed to load Map View',
      getProps: (route) => ({ workspaceId: route.params.id, collectionId: route.params.collectionId })
    },
    'workspace-roadmap': {
      loadingMsg: 'Loading Roadmap...',
      errorMsg: 'Failed to load Roadmap',
      getProps: (route) => ({ workspaceId: route.params.id, collectionId: route.params.collectionId })
    },
    'workspace-pages': {
      loadingMsg: 'Loading Pages...',
      errorMsg: 'Failed to load Pages',
      wrapper: 'none',
      getProps: (route) => ({
        workspaceId: Number(route.params.id),
        pageId: route.params.pageId ? Number(route.params.pageId) : null,
      })
    },
    'workspace-pages-archived': {
      loadingMsg: 'Loading Archived Pages...',
      errorMsg: 'Failed to load Archived Pages',
      wrapper: 'none',
      getProps: (route) => ({ workspaceId: Number(route.params.id) })
    },
    // Global collection views (no workspace)
    'collection-board': {
      loadingMsg: 'Loading Board View...',
      errorMsg: 'Failed to load Board View',
      getProps: (route) => ({ workspaceId: null, collectionId: route.params.id })
    },
    'collection-board-config': {
      loadingMsg: 'Loading Board Configuration...',
      errorMsg: 'Failed to load Board Configuration',
      getProps: (route) => ({ workspaceId: null, collectionId: route.params.id })
    },
    'collection-backlog': {
      loadingMsg: 'Loading Backlog View...',
      errorMsg: 'Failed to load Backlog View',
      getProps: (route) => ({ workspaceId: null, collectionId: route.params.id })
    },
    'collection-list': {
      loadingMsg: 'Loading List View...',
      errorMsg: 'Failed to load List View',
      getProps: (route) => ({ workspaceId: null, collectionId: route.params.id })
    },
    'collection-tree': {
      loadingMsg: 'Loading Tree View...',
      errorMsg: 'Failed to load Tree View',
      getProps: (route) => ({ workspaceId: null, collectionId: route.params.id })
    },
    'collection-map': {
      loadingMsg: 'Loading Map View...',
      errorMsg: 'Failed to load Map View',
      getProps: (route) => ({ workspaceId: null, collectionId: route.params.id })
    },
    'collection-roadmap': {
      loadingMsg: 'Loading Roadmap...',
      errorMsg: 'Failed to load Roadmap',
      getProps: (route) => ({ workspaceId: null, collectionId: route.params.id })
    },
    'workspace-iterations': {
      loadingMsg: 'Loading Iterations...',
      errorMsg: 'Failed to load Iterations',
      wrapper: 'surface-full',
      getProps: (route) => ({ workspaceId: route.params.id })
    },
    'workspace-milestones': {
      loadingMsg: 'Loading Milestones...',
      errorMsg: 'Failed to load Milestones',
      wrapper: 'surface-full',
      getProps: (route) => ({ workspaceId: route.params.id })
    },
    'workspace-actions': {
      loadingMsg: 'Loading Actions...',
      errorMsg: 'Failed to load Actions',
      wrapper: 'none',
      getProps: (route) => ({
        workspaceId: route.params.id,
        actionId: Number(route.params.actionId) || 0,
      })
    },
    'workspace-analytics': {
      loadingMsg: 'Loading Analytics...',
      errorMsg: 'Failed to load Analytics',
      wrapper: 'surface-full',
      getProps: (route) => ({ workspaceId: route.params.id })
    },
    'command-palette': {
      trigger: 'showCommandPalette'
    },
    'create-modal': {
      trigger: 'showCreateModal'
    },
    'homepage': {
      loadingMsg: 'Loading Homepage...',
      errorMsg: 'Failed to load Homepage',
      wrapper: 'surface-full'
    },
    'licenses': {
      loadingMsg: 'Loading Licenses...',
      errorMsg: 'Failed to load Licenses',
      wrapper: 'surface-full'
    },
    'item-detail': {
      loadingMsg: 'Loading Item Details...',
      errorMsg: 'Failed to load Item Details',
      getProps: (route) => {
        const personal = route.path.startsWith('/personal');
        const workspaceParam = route.params.workspaceKey || route.params.id;
        const itemParam = route.params.itemKey || route.params.itemNumber || route.params.itemId;
        const fullKeyMatch = !personal ? String(itemParam || '').match(/^([^/\s-]+)-(\d+)$/) : null;
        const workspaceParamIsKey = !!workspaceParam && !/^\d+$/.test(String(workspaceParam));
        const keyWorkspace = fullKeyMatch?.[1] || (workspaceParamIsKey ? workspaceParam : null);
        const keyItemNumber = fullKeyMatch?.[2] || (keyWorkspace ? itemParam : null);
        return {
          workspaceId: personal ? $workspacesStore.personalWorkspace?.id : (keyWorkspace ? null : workspaceParam),
          itemId: keyItemNumber || itemParam,
          workspaceKey: !personal ? keyWorkspace : null,
          itemNumber: !personal ? keyItemNumber : null,
          canonicalizeKeyRoute: !personal && !!keyWorkspace,
          tab: route.query.tab || 'comments',
          moduleSettings: $moduleSettings
        };
      }
    },
    'personal-task-detail': {
      loadingMsg: 'Loading Task...',
      errorMsg: 'Failed to load Task',
      getProps: (route) => ({
        workspaceId: route.path.startsWith('/personal') ? $workspacesStore.personalWorkspace?.id : route.params.id,
        itemId: route.params.itemId,
        isModal: false
      })
    },
    'workspace-detail': {
      loadingMsg: 'Loading Workspace...',
      errorMsg: 'Failed to load Workspace',
      getProps: (route) => ({ workspaceId: route.params.id, collectionId: route.params.collectionId })
    },
    'workspace-overview': {
      loadingMsg: 'Loading Workspace...',
      errorMsg: 'Failed to load Workspace',
      getProps: (route) => ({ workspaceId: route.params.id, collectionId: route.params.collectionId })
    },
    'personal-workspace': {
      loadingMsg: 'Loading Personal Workspace...',
      errorMsg: 'Failed to load Personal Workspace',
      getProps: () => ({ workspaceId: $workspacesStore.personalWorkspace?.id })
    },
    'workspace-calendar': {
      loadingMsg: 'Loading Calendar...',
      errorMsg: 'Failed to load Calendar',
      wrapper: 'surface-full',
      getProps: (route) => ({
        workspaceId: route.path.startsWith('/personal') ? $workspacesStore.personalWorkspace?.id : route.params.id
      })
    },
    'workspace-reviews': {
      loadingMsg: 'Loading Reviews...',
      errorMsg: 'Failed to load Reviews',
      wrapper: 'surface-full',
      getProps: (route) => ({
        currentUser: authStore.currentUser,
        workspaceId: route.path.startsWith('/personal') ? $workspacesStore.personalWorkspace?.id : route.params.id
      })
    },
    'workflow-designer': {
      loadingMsg: 'Loading workflow designer...',
      errorMsg: 'Failed to load workflow designer'
    },
    'configuration-set-detail': {
      loadingMsg: 'Loading configuration set...',
      errorMsg: 'Failed to load configuration set'
    },
    'workspace-look-and-feel': {
      loadingMsg: 'Loading Look and Feel...',
      errorMsg: 'Failed to load Look and Feel',
      wrapper: 'none',
      getProps: (route) => ({ workspaceId: route.params.id })
    },
    'personal-plan': {
      loadingMsg: 'Loading Plan My Day...',
      errorMsg: 'Failed to load Plan My Day',
      wrapper: 'surface-full'
    },
    'logbook': {
      loadingMsg: 'Loading Knowledge Base...',
      errorMsg: 'Failed to load Knowledge Base',
      wrapper: 'surface-full'
    },
    'logbook-document': {
      loadingMsg: 'Loading Document...',
      errorMsg: 'Failed to load Document',
      wrapper: 'surface-full',
      getProps: (route) => ({ documentId: route.params.documentId })
    }
  };

  const testViews = new Set([
    'test-cases',
    'test-sets',
    'test-templates',
    'test-runs',
    'test-reports',
    'test-steps',
    'test-run-detail',
    'test-template-detail',
    'test-execution',
    'test-case-detail',
    'test-set-detail'
  ]);

  function resolveRouteConfig(view) {
    if (!view) {
      return { key: null, config: null };
    }

    if (routeConfig[view]) {
      return { key: view, config: routeConfig[view] };
    }

    for (const [key, config] of Object.entries(routeConfig)) {
      if (config.matchViews?.includes(view)) {
        return { key, config };
      }
    }

    return { key: null, config: null };
  }

  // Compute the effective view, replacing item-detail with personal-task-detail for personal workspaces
  let effectiveView = $derived.by(() => {
    const view = $currentRoute.view;

    // Check if this is an item-detail view and if the workspace is personal
    if (view === 'item-detail') {
      const workspaceId = $currentRoute.path?.startsWith('/personal')
        ? $workspacesStore.personalWorkspace?.id
        : parseInt($currentRoute.params?.id);

      // Check if this workspace is the personal workspace
      if ($workspacesStore.personalWorkspace?.id && workspaceId === $workspacesStore.personalWorkspace?.id) {
        return 'personal-task-detail';
      }
    }

    return view;
  });

  let showWorkspaceNav = $derived(
    !$uiStore.reviewFullscreen &&
    $currentRoute.view !== 'workspaces' &&
    !!$currentWorkspace &&
    (isWorkspaceRoute($currentRoute.view) || effectiveView === 'personal-task-detail' || testViews.has($currentRoute.view))
  );

  let showCollectionNav = $derived(
    !$uiStore.reviewFullscreen && GLOBAL_COLLECTION_VIEWS.has($currentRoute.view)
  );

  let routeProps = $derived.by(() => getPropsForRoute(effectiveView));

  onMount(() => {
    // Initialize activity tracking for adaptive polling
    activityStore.init();

    // Desktop (Tauri) clients: refresh open views on window/tab focus so
    // backend changes (CLI edits, other users, agents) appear without a
    // manual reload. Browser-only no-op.
    initDesktopFocusRefresh();
    desktopBridge.init();

    // Start the shared notification poller (feeds tray, toasts, and the
    // new-notification bus used by item views to refresh instantly).
    startNotificationPoller();

    // MainApp only renders after authentication. Start the small navigation /
    // permission critical path together with every optional capability probe;
    // optional work must not delay the first useful route.
    const user = authStore.currentUser;
    const userId = authStore.currentUser?.id;
    const criticalTasks = [
      () => workspacesStore.load(),
      () => permissionStore.loadAllPermissions(user),
    ];
    if (userId) {
      criticalTasks.push(
        () => permissionStore.loadUserPermissions(userId),
        () => workspacePermissions.loadPermissions(userId),
      );
    }

    const deferredTasks = [
      () => workspacesStore.loadPersonalWorkspace(),
      async () => {
        try {
          const bootstrap = await api.shellBootstrap.get();
          moduleSettings.hydrate(bootstrap.module_settings);
          attachmentStatus.hydrate(bootstrap.attachment_status);
          aiStore.hydrate(bootstrap.ai);
          capabilitiesStore.hydrate(bootstrap.features);
          logbookStore.hydrateAvailability(bootstrap.features?.logbook_available);
          permissionStore.setLogbookAvailable(bootstrap.features?.logbook_available === true);
          permissionStore.setHasAssetSets(bootstrap.has_asset_sets === true);
          permissionStore.setHasActivePortals(bootstrap.has_active_portals === true);
        } catch (err) {
          capabilitiesStore.failHydration();
          console.warn('Failed to load shell capabilities:', err);
        }
      },
      async () => {
        if (ssoStore.checkForEmailVerificationPending()) {
          showEmailVerificationBanner = true;
          return;
        }
        try {
          const status = await ssoStore.getVerificationStatus();
          showEmailVerificationBanner = Boolean(status.configured && !status.email_verified);
        } catch (err) {
          console.warn('Failed to check email verification status:', err);
        }
      },
    ];

    void runAuthenticatedShellBootstrap({
      userId,
      criticalTasks,
      deferredTasks,
      onMeasured: (metrics) => {
        window.dispatchEvent(new CustomEvent('windshift:auth-shell-bootstrap', { detail: metrics }));
      },
    });

    return () => {
      stopNotificationPoller();
      homepageStore.reset();
      resetAuthenticatedShellState();
    };
  });

  // Double-space handler for command palette (manual — not a simple hotkey)
  useEventListener(() => document, 'keydown', (e) => {
    if (e.code !== 'Space') return;

    const target = /** @type {HTMLElement} */ (e.target);
    const isInInputField = target.tagName === 'INPUT' ||
                          target.tagName === 'TEXTAREA' ||
                          target.contentEditable === 'true' ||
                          target.closest('[contenteditable="true"]');
    if (isInInputField) return;

    const now = Date.now();
    if (now - lastSpaceTime < DOUBLE_SPACE_THRESHOLD) {
      e.preventDefault();
      showCommandPalette = true;
    } else {
      e.preventDefault();
    }
    lastSpaceTime = now;
  });

  // Listen for create modal events and workspace refresh from other components
  onMount(() => {
    function handleShowCreateModal(event) {
      const detail = event.detail || {};

      if (detail.type) {
        createModalInitialType = detail.type;
      }
      createModalSkipNavigate = detail.skipNavigate || false;
      createModalWorkspaceId = detail.workspaceId
        ? Number.parseInt(String(detail.workspaceId), 10)
        : null;

      showCreateModal = true;
    }

    window.addEventListener('show-create-modal', handleShowCreateModal);

    function handleRefreshWorkspaces() {
      workspacesStore.reload();
    }

    function handleRefreshWorkspaceData() {
      workspaceDataStore.refresh();
    }

    function handleRefreshWorkItems() {
      homepageStore.invalidateSnapshot();
    }

    window.addEventListener('refresh-workspaces', handleRefreshWorkspaces);
    window.addEventListener('refresh-workspace-data', handleRefreshWorkspaceData);
    window.addEventListener('refresh-work-items', handleRefreshWorkItems);

    return () => {
      window.removeEventListener('show-create-modal', handleShowCreateModal);
      window.removeEventListener('refresh-workspaces', handleRefreshWorkspaces);
      window.removeEventListener('refresh-workspace-data', handleRefreshWorkspaceData);
      window.removeEventListener('refresh-work-items', handleRefreshWorkItems);
    };
  });



  async function hydrateCurrentWorkspaceFromSharedData(workspaceId) {
    await workspaceDataStore.initialize(workspaceId);
    const expectedId = Number.parseInt(String(workspaceId), 10);
    if (workspaceDataStore.workspaceId !== expectedId || !workspaceDataStore.workspace) return;
    currentWorkspace.hydrate(workspaceDataStore.workspace);
  }

  // Load current workspace when route changes (only for workspace routes).
  // workspaceDataStore owns the request; currentWorkspace is hydrated from the
  // same response so the shell does not issue an identical workspace GET.
  $effect(() => {
    // Handle personal workspace routes
    if ($currentRoute.path?.startsWith('/personal') && ($currentRoute.view?.startsWith('workspace-') || $currentRoute.view === 'personal-workspace' || $currentRoute.view === 'personal-plan' || $currentRoute.view === 'item-detail')) {
      const personalWorkspaceId = $workspacesStore.personalWorkspace?.id;
      if (personalWorkspaceId) {
        void hydrateCurrentWorkspaceFromSharedData(personalWorkspaceId);
      } else {
        // Personal workspace not loaded yet - trigger loading
        // The effect will re-run when personalWorkspace is set in the store
        workspacesStore.loadPersonalWorkspace();
      }
    }
    // Handle regular workspace routes
    else if ($currentRoute.params?.id && /^\d+$/.test(String($currentRoute.params.id)) && ($currentRoute.view?.startsWith('workspace-') || $currentRoute.view === 'workspace' || $currentRoute.view === 'item-detail' || $currentRoute.view === 'item' || testViews.has($currentRoute.view))) {
      void hydrateCurrentWorkspaceFromSharedData($currentRoute.params.id);
    }
    // Handle global collection views (no workspace)
    else if (GLOBAL_COLLECTION_VIEWS.has($currentRoute.view)) {
      if ($currentWorkspace) currentWorkspace.clear();
      workspaceDataStore.initializeGlobal();
      clearWorkspaceGradient();
    } else {
      currentWorkspace.clear();
      workspaceDataStore.reset();
      clearWorkspaceGradient();
    }
  });

  // Collection store is self-activating via route subscription in constructor — no need to manually activate

  // Redirect from workspace-detail to the configured default view
  $effect(() => {
    if ($currentRoute.view === 'workspace-detail' && $currentWorkspace) {
      const wsId = $currentRoute.params?.id;
      if (wsId) {
        const defaultView = $currentWorkspace.default_view || 'board';
        if (defaultView === 'overview') {
          navigate(`/workspaces/${wsId}/overview`, { replace: true });
        } else {
          navigate(`/workspaces/${wsId}/${defaultView}`, { replace: true });
        }
      }
    }
  });

  // Load terminal panel when terminal becomes visible
  $effect(() => {
    if (terminalState.visible && !TerminalPanelComponent) {
      loadTerminalPanel();
    }
  });

  $effect(() => {
    if (desktopBridge.modal) {
      loadDesktopModal(desktopBridge.modal);
    }
  });

  // Single effect to load components based on current route
  $effect(() => {
    const view = effectiveView;

    // Load component for current route view
    if (view) {
      const { key } = resolveRouteConfig(view);
      if (key) {
        loadComponentForRoute(key);
      }
    }

    // Load command palette when opened
    if (showCommandPalette) {
      loadComponentForRoute('command-palette');
    }

    // Load create modal when opened
    if (showCreateModal) {
      loadComponentForRoute('create-modal');
    }
  });



  function showCreateDropdown() {
    createModalWorkspaceId = null;

    // Pre-select current workspace if we're in a workspace context
    const currentWorkspaceId = $currentRoute.params?.id;
    if (currentWorkspaceId && ['workspace-detail', 'workspace-calendar', 'workspace-reviews', 'workspace-settings', 'workspace-settings-general', 'workspace-settings-categories', 'workspace-settings-members', 'workspace-settings-configuration', 'workspace-settings-source-control', 'workspace-settings-coding-agents', 'workspace-settings-issue-sync', 'workspace-settings-templates', 'workspace-settings-danger', 'workspace-look-and-feel', 'workspace-board', 'workspace-backlog', 'workspace-list', 'workspace-tree', 'workspace-map', 'workspace-roadmap', 'workspace-actions', 'item-detail'].includes($currentRoute.view)) {
      createModalWorkspaceId = Number.parseInt(currentWorkspaceId, 10);
    }
    showCreateModal = true;
  }

  // Generic lazy loader function for all routes
  async function loadComponentForRoute(view) {
    return lazyComponents.load(view);
  }

  function retryComponentForRoute(view) {
    return lazyComponents.retry(view);
  }

  // Helper to get component for current view (supports matchViews)
  function getComponentForView(view) {
    // Direct match
    if (lazyComponents.getComponent(view)) {
      return lazyComponents.getComponent(view);
    }

    const { key } = resolveRouteConfig(view);
    if (key && lazyComponents.getComponent(key)) {
      return lazyComponents.getComponent(key);
    }

    return null;
  }

  // Helper to check if component is loading
  function isComponentLoading(view) {
    if (lazyComponents.isLoading(view)) return true;

    const { key } = resolveRouteConfig(view);
    if (key && lazyComponents.isLoading(key)) {
      return true;
    }

    return false;
  }

  function getComponentLoadError(view) {
    if (lazyComponents.getError(view)) return lazyComponents.getError(view);

    const { key } = resolveRouteConfig(view);
    if (key) return lazyComponents.getError(key);

    return null;
  }

  // Get props for current route's component
  function getPropsForRoute(view) {
    const { config } = resolveRouteConfig(view);
    if (!config || !config.getProps) return {};
    return config.getProps($currentRoute);
  }

  // Check if a route requires wrapper styling
  function getWrapperClass(view) {
    const { config } = resolveRouteConfig(view);
    return config?.wrapper || null;
  }
</script>

{#snippet loadingState(message)}
  <div class="flex items-center justify-center h-full">
    <div class="text-center">
      <Spinner class="mx-auto mb-4" />
      <p class="text-gray-600">{message}</p>
    </div>
  </div>
{/snippet}

{#snippet errorState(message, retryFn)}
  <div class="flex items-center justify-center h-full">
    <div class="text-center">
      <p class="text-red-600">{message}</p>
      {#if retryFn}
        <Button variant="primary" onclick={retryFn} class="mt-4">
          {t('nav.retry')}
        </Button>
      {/if}
    </div>
  </div>
{/snippet}

{#snippet lazyLoadedComponent(view, props)}
  {@const component = getComponentForView(view)}
  {@const loading = isComponentLoading(view)}
  {@const loadError = getComponentLoadError(view)}
  {@const routeEntry = resolveRouteConfig(view)}
  {@const config = routeEntry.config}
  {@const loaderKey = routeEntry.key || view}

  {#if loading}
    {@render loadingState(config?.loadingMsg || 'Loading...')}
  {:else if component}
    {@const LazyComponent = component}
    <LazyComponent {...props} />
  {:else if loadError}
    {@render errorState(config?.errorMsg || 'Failed to load component', () => retryComponentForRoute(loaderKey))}
  {:else}
    {@render loadingState(config?.loadingMsg || 'Loading...')}
  {/if}
{/snippet}

<!-- Main Internal App - Rendered only when user is authenticated -->
<div class="min-h-screen flex flex-col" style="background-color: var(--ds-surface);">
  <!-- Email Verification Banner -->
  <EmailVerificationBanner
    show={showEmailVerificationBanner}
    ondismiss={() => showEmailVerificationBanner = false}
  />

  <!-- Vertical Left Sidebar Navigation -->
  {#if !$uiStore.reviewFullscreen}
    <MainSidebar
      onShowCommandPalette={() => showCommandPalette = true}
      onShowCreateModal={showCreateDropdown}
      onShowChatPanel={() => { showChatPanel = true; loadComponentForRoute('chat-panel'); }}
      onToggleTerminal={toggleTerminal}
    />
  {/if}

  <!-- Hidden hotkey buttons for global shortcuts -->
  <Button class="sr-only" onclick={() => showCommandPalette = true} hotkeyConfig={{ key: toHotkeyString('global', 'commandPalette') }}>Command Palette</Button>
  <Button class="sr-only" onclick={showCreateDropdown} hotkeyConfig={{ key: toHotkeyString('global', 'create') }}>Create</Button>
  {#if aiStore.chatAvailable}
    <Button class="sr-only" onclick={() => { showChatPanel = !showChatPanel; loadComponentForRoute('chat-panel'); }} hotkeyConfig={{ key: toHotkeyString('global', 'aiChat') }}>AI Chat</Button>
  {/if}
  <Button class="sr-only" onclick={toggleTerminal} hotkeyConfig={{ key: 'Mod+`' }}>Toggle Terminal</Button>

    <!-- Main Content Area with Sidebar Layout -->
    <div
      class="flex flex-1 transition-[margin] duration-200 ease-out"
      style={!$uiStore.reviewFullscreen ? `margin-left: ${$uiStore.navExpanded ? '200px' : '64px'}` : ''}
    >
      <!-- Left Sidebar for Workspace/Admin Navigation -->
      {#if showWorkspaceNav}
        <div out:slide={{ duration: 200, axis: 'x' }}>
          <WorkspaceNavigation workspaceId={$currentRoute.path?.startsWith('/personal') ? $workspacesStore.personalWorkspace?.id : $currentRoute.params.id} />
        </div>
      {:else if showCollectionNav}
        <div out:slide={{ duration: 200, axis: 'x' }}>
          <CollectionNavigation collectionId={$currentRoute.params.id} />
        </div>
      {/if}

      <!-- Main Content Column (with optional terminal split) -->
      <div class="flex-1 flex min-w-0 main-split-container">
        <!-- Left Pane: Main Content -->
        <div
          class="flex flex-col min-w-0"
          style={terminalState.visible ? `width: ${terminalState.splitPercent}%; flex-shrink: 0;` : 'flex: 1;'}
        >
        <!-- Main Content -->
        <main class="flex-1">
    {#if true}
      {@const view = effectiveView}
      {@const wrapper = getWrapperClass(view)}
      {@const routeEntry = resolveRouteConfig(view)}
      {@const hasLazyRoute = !!routeEntry.config}

      {#if view === 'workspaces'}
      <Workspaces showAdminHeader={false} />

    {:else if ['workspace-settings', 'workspace-settings-general', 'workspace-settings-categories', 'workspace-settings-members', 'workspace-settings-configuration', 'workspace-settings-source-control', 'workspace-settings-coding-agents', 'workspace-settings-issue-sync', 'workspace-settings-recurrence', 'workspace-settings-templates', 'workspace-settings-danger'].includes(view)}
      <div class="p-6" style="background-color: var(--ds-surface);">
        <WorkspaceSettings
          workspaceId={$currentRoute.params.id}
          activeTab={
            view === 'workspace-settings-categories' ? 'categories' :
            view === 'workspace-settings-members' ? 'members' :
            view === 'workspace-settings-configuration' ? 'configuration' :
            view === 'workspace-settings-source-control' ? 'source-control' :
            view === 'workspace-settings-coding-agents' ? 'coding-agents' :
            view === 'workspace-settings-issue-sync' ? 'issue-sync' :
            view === 'workspace-settings-recurrence' ? 'recurrence' :
            view === 'workspace-settings-templates' ? 'templates' :
            view === 'workspace-settings-danger' ? 'danger' :
            'general'
          }
        />
      </div>

    {:else if view === 'collections-list'}
      <CollectionsList />
    {:else if view === 'collections-edit'}
      <Collections collectionId={$currentRoute.params.id} />

    {:else if view === 'channels'}
      <div style="background-color: var(--ds-surface);">
        <Channels />
      </div>
    {:else if view === 'hub' || view === 'hub-inbox'}
      <div style="background-color: var(--ds-surface);">
        <Hub />
      </div>
    {:else if view === 'organizations' || view === 'organization-contact-detail'}
      <Customers />

    {:else if view === 'teams-list'}
      <div class="p-6" style="background-color: var(--ds-surface);">
        <TeamsList />
      </div>
    {:else if view === 'team-detail'}
      <div class="p-6" style="background-color: var(--ds-surface);">
        <TeamDetail teamId={$currentRoute.params.id} section={$currentRoute.params.section || 'overview'} />
      </div>

    {:else if view === 'notifications'}
      <NotificationsPage />
    {:else if view === 'approvals-inbox'}
      <ApprovalsInbox />
    {:else if view === 'search'}
      <SearchPage />

    {:else if view === 'profile'}
      <div class="p-6" style="background-color: var(--ds-surface);">
        <UserProfile />
      </div>
    {:else if view === 'security'}
      <div class="p-6" style="background-color: var(--ds-surface);">
        <Security />
      </div>

    {:else if view === 'about'}
      <About />
    {:else if view === 'api-docs'}
      <ApiDocs />
    {:else if view === 'cli-authorize'}
      <CliAuthorize />
    {:else if view === 'oauth-authorize'}
      <OAuthAuthorize />
    {:else if view === '404'}
      <div class="p-6" style="background-color: var(--ds-surface);">
        <NotFound />
      </div>

    {:else if view === 'admin'}
      {#if $currentRoute.path.startsWith('/admin/channels')}
        {@render lazyLoadedComponent(view, routeProps)}
      {:else}
        <PermissionGuard requireSystemAdmin={true}>
          {#snippet children()}
            {@render lazyLoadedComponent(view, routeProps)}
          {/snippet}
          {#snippet fallback(requiredPermissionDisplay)}
            <UnauthorizedAccess
              message="You need system administrator privileges to access the administration panel."
              requiredPermission={requiredPermissionDisplay}
            />
          {/snippet}
        </PermissionGuard>
      {/if}

    {:else if view === 'workspace-actions'}
      <div class="h-full" style="background-color: var(--ds-surface); height: calc(100vh - 56px);">
        {@render lazyLoadedComponent(view, routeProps)}
      </div>

    {:else if hasLazyRoute}
      {#if wrapper === 'surface-full'}
        <div style="background-color: var(--ds-surface);">
          {@render lazyLoadedComponent(view, routeProps)}
        </div>
      {:else if wrapper === 'surface-padded'}
        <div class="p-6" style="background-color: var(--ds-surface);">
          {@render lazyLoadedComponent(view, routeProps)}
        </div>
      {:else if wrapper === 'surface-admin'}
        <div class="px-16 py-12 flex-1 overflow-y-auto" style="background-color: var(--ds-surface);">
          {@render lazyLoadedComponent(view, routeProps)}
        </div>
      {:else}
        {@render lazyLoadedComponent(view, routeProps)}
      {/if}

      {:else}
        <Workspaces showAdminHeader={false} />
      {/if}
    {/if}
      </main>
      </div>

        {#if terminalState.visible}
          <!-- Resize Handle -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="terminal-resize-handle w-1 cursor-col-resize hover:bg-blue-500/40 active:bg-blue-500/60 transition-colors flex-shrink-0"
            style="background-color: var(--ds-border);"
            onmousedown={handleTerminalResizeStart}
          ></div>

          <!-- Right Pane: Terminal -->
          <div
            class="flex flex-col min-w-0"
            style="width: {100 - terminalState.splitPercent}%; flex-shrink: 0;"
          >
            {#if TerminalPanelComponent}
              <TerminalPanelComponent />
            {:else if terminalLoading}
              <div class="flex items-center justify-center h-full" style="background-color: #1a1b26;">
                <Spinner />
              </div>
            {/if}
          </div>
        {/if}
      </div>
    </div>
    
    <!-- Footer with proper sidebar margin -->
    <footer
      class="transition-transform duration-200 ease-out {!$uiStore.reviewFullscreen ? 'ml-16' : ''}"
      style={!$uiStore.reviewFullscreen ? `transform: translateX(${$uiStore.navExpanded ? '136px' : '0px'})` : ''}
    >
      <Footer />
    </footer>
</div>


<!-- Command Palette -->
{#if true}
  {@const commandPaletteComponent = getComponentForView('command-palette')}
  {@const commandPaletteLoading = isComponentLoading('command-palette')}
  {@const commandPaletteError = getComponentLoadError('command-palette')}

  {#if commandPaletteLoading}
    <ModalBackdrop show={true} opacity={0.4} blur={8} extraFilter="saturate(120%)" zIndex={60} align="top" paddingTop="pt-[20vh]" closeOnClick={false} closeOnEscape={false} transition={false}>
      <div class="rounded-xl p-6" style="background-color: var(--ds-surface-raised); color: var(--ds-text-subtle);">
        <Spinner class="mx-auto mb-4" />
        <p>{t('nav.loadingSearch')}</p>
      </div>
    </ModalBackdrop>
  {:else if commandPaletteComponent && showCommandPalette}
    {@const CommandPalette = commandPaletteComponent}
    <CommandPalette
      bind:isOpen={showCommandPalette}
      onclose={() => showCommandPalette = false}
    />
  {:else if commandPaletteError && showCommandPalette}
    <!-- shortcut-guard-exempt: retrying a failed lazy import is a recovery action, not a form submission. -->
    <ModalBackdrop show={true} opacity={0.4} zIndex={60} closeOnClick={false} onclose={() => showCommandPalette = false}>
      <div class="rounded-xl p-6 text-center" role="alert" style="background-color: var(--ds-surface-raised); color: var(--ds-text);">
        <p class="font-semibold">Failed to load Search</p>
        <p class="mt-1 text-sm" style="color: var(--ds-text-subtle);">Check your connection, then try again.</p>
        <div class="mt-4 flex justify-center gap-2">
          <Button variant="secondary" onclick={() => showCommandPalette = false}>{t('common.close')}</Button>
          <Button variant="primary" onclick={() => retryComponentForRoute('command-palette')}>{t('nav.retry')}</Button>
        </div>
      </div>
    </ModalBackdrop>
  {/if}
{/if}

<!-- Create Modal -->
{#if true}
  {@const createModalComponent = getComponentForView('create-modal')}
  {@const createModalLoading = isComponentLoading('create-modal')}
  {@const createModalError = getComponentLoadError('create-modal')}

  {#if createModalLoading}
    <ModalBackdrop show={true} opacity={0.4} closeOnClick={false} closeOnEscape={false} transition={false}>
      <div class="rounded-xl p-6" style="background-color: var(--ds-surface-raised); color: var(--ds-text-subtle);">
        <Spinner class="mx-auto mb-4" />
        <p>{t('nav.loadingCreateForm')}</p>
      </div>
    </ModalBackdrop>
  {:else if createModalComponent && showCreateModal}
    {@const CreateModal = createModalComponent}
    <CreateModal
      bind:isOpen={showCreateModal}
      initialType={createModalInitialType}
      initialWorkspaceId={createModalWorkspaceId}
      skipNavigate={createModalSkipNavigate}
      onclose={closeCreateModal}
    />
  {:else if createModalError && showCreateModal}
    <!-- shortcut-guard-exempt: retrying a failed lazy import is a recovery action, not a form submission. -->
    <ModalBackdrop show={true} opacity={0.4} closeOnClick={false} onclose={closeCreateModal}>
      <div class="rounded-xl p-6 text-center" role="alert" data-testid="create-modal-load-error" style="background-color: var(--ds-surface-raised); color: var(--ds-text);">
        <p class="font-semibold">Failed to load Create Form</p>
        <p class="mt-1 text-sm" style="color: var(--ds-text-subtle);">Check your connection, then try again.</p>
        <div class="mt-4 flex justify-center gap-2">
          <Button variant="secondary" onclick={closeCreateModal}>{t('common.close')}</Button>
          <Button variant="primary" onclick={() => retryComponentForRoute('create-modal')}>{t('nav.retry')}</Button>
        </div>
      </div>
    </ModalBackdrop>
  {/if}
{/if}

<!-- Global Confirmation Dialog -->
<GlobalConfirmDialog />

{#if desktopBridge.modal === 'pomodoro-settings' && PomodoroSettingsModalComponent}
  <PomodoroSettingsModalComponent
    show={true}
    onclose={() => desktopBridge.close()}
  />
{:else if desktopBridge.modal === 'about' && AboutModalComponent}
  <AboutModalComponent
    show={true}
    onclose={() => desktopBridge.close()}
  />
{/if}

<!-- Floating Timer -->
<FloatingTimer />

<!-- AI Chat Panel -->
{#if aiStore.chatAvailable && showChatPanel}
  {@const ChatPanelComponent = getComponentForView('chat-panel')}
  {#if ChatPanelComponent}
    <ChatPanelComponent
      bind:isOpen={showChatPanel}
      onclose={() => { showChatPanel = false; chatStore.clearHistory(); }}
    />
  {/if}
{/if}

<!-- Toast Container -->
<ToastContainer />

<style>
  /* Global CSS custom properties for theming - uses design tokens */
  :global(html) {
    --nav-bg-color: var(--ds-surface-raised);
    --nav-text-color: var(--ds-text);
  }

  /* Themed navigation styles */
  :global(.themed-nav) {
    background-color: var(--nav-bg-color);
    color: var(--nav-text-color);
  }

  /* Ensure child elements inherit the theme colors */
  :global(.themed-nav *) {
    color: inherit;
  }

  /* Override any specific text colors for navigation elements */
  :global(.themed-nav a),
  :global(.themed-nav button) {
    color: var(--nav-text-color);
  }

  /* Theme-aware navigation button classes with enhanced animations */
  :global(.themed-nav .nav-button) {
    color: var(--nav-text-color);
    position: relative;
    transition:
      background-color var(--duration-normal, 200ms) var(--ease-smooth, ease),
      box-shadow var(--duration-normal, 200ms) var(--ease-smooth, ease);
  }

  /* Subtle glow effect on hover */
  :global(.themed-nav .nav-button::before) {
    content: '';
    position: absolute;
    inset: -2px;
    border-radius: 8px;
    background: radial-gradient(
      circle at center,
      var(--ds-interactive) 0%,
      transparent 70%
    );
    opacity: 0;
    transition: opacity var(--duration-normal, 200ms) var(--ease-smooth, ease);
    pointer-events: none;
    z-index: -1;
  }

  :global(.themed-nav .nav-button:hover) {
    background-color: var(--ds-background-neutral-hovered);
  }

  :global(.themed-nav .nav-button:hover::before) {
    opacity: 0.12;
  }

  :global(.themed-nav .nav-button.nav-button-emphasized) {
    background-color: color-mix(in srgb, var(--ds-interactive) 8%, transparent);
  }

  /* Exception: Primary buttons should keep their original colors and hover behavior */
  :global(.themed-nav .bg-primary) {
    color: var(--ds-text-inverse) !important;
    background-color: var(--ds-interactive) !important;
    transition:
      background-color var(--duration-normal, 200ms) var(--ease-smooth, ease),
      transform var(--duration-fast, 100ms) var(--ease-spring, cubic-bezier(0.34, 1.56, 0.64, 1)),
      box-shadow var(--duration-normal, 200ms) var(--ease-smooth, ease);
  }

  :global(.themed-nav .bg-primary:hover) {
    background-color: var(--ds-interactive-hovered) !important;
    transform: scale(1.05);
    box-shadow: var(--ds-glow-primary);
  }

  :global(.themed-nav .bg-primary:active) {
    transform: scale(0.95);
  }

  /* Reduced motion support */
  @media (prefers-reduced-motion: reduce) {
    :global(.themed-nav .nav-button),
    :global(.themed-nav .bg-primary) {
      transition: none;
    }
    :global(.themed-nav .bg-primary:hover),
    :global(.themed-nav .bg-primary:active) {
      transform: none;
    }
  }

  /* Terminal resize handle */
  .terminal-resize-handle {
    position: relative;
    z-index: 10;
  }

  .terminal-resize-handle::before {
    content: '';
    position: absolute;
    top: 0;
    bottom: 0;
    left: -3px;
    right: -3px;
  }
</style>
