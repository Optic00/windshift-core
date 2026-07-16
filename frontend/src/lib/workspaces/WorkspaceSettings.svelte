<script>
  import { onMount } from 'svelte';
  import { api } from '../api.js';
  import { navigate } from '../router.js';
  import { workspacePermissions, workspacesStore, currentWorkspace } from '../stores';
  import { Trash2, AlertTriangle, Clock, Shield } from '@lucide/svelte';
  import { moduleSettings } from '../stores/moduleSettings.js';
  import WorkspaceConfigurationAssigner from './WorkspaceConfigurationAssigner.svelte';
  import WorkspaceConfigurationPreview from './WorkspaceConfigurationPreview.svelte';
  import WorkspaceSCMSettings from './WorkspaceSCMSettings.svelte';
  import WorkspaceAgentBindings from './WorkspaceAgentBindings.svelte';
  import WorkspaceAgentSkills from './WorkspaceAgentSkills.svelte';
  import IssueSyncSettings from '../settings/IssueSyncSettings.svelte';
  import RecurrenceManager from '../settings/RecurrenceManager.svelte';
  import WorkspaceItemTemplates from './WorkspaceItemTemplates.svelte';
  import Button from '../components/Button.svelte';
  import PageHeader from '../layout/PageHeader.svelte';
  import Input from '../components/Input.svelte';
  import Select from '../components/Select.svelte';
  import Textarea from '../components/Textarea.svelte';
  import CategoryMultiSelect from '../pickers/CategoryMultiSelect.svelte';
  import WorkspaceMembers from './WorkspaceMembers.svelte';
  import AlertBox from '../components/AlertBox.svelte';
  import Label from '../components/Label.svelte';
  import Toggle from '../components/Toggle.svelte';
  import Card from '../components/Card.svelte';
  import { workspaceSettingsItems } from '../navigation/workspaceNavigation.js';
  import { successToast, errorToast } from '../stores/toasts.svelte.js';
  import { t } from '../stores/i18n.svelte.js';
  import DescriptionText from '../components/DescriptionText.svelte';

  let { workspaceId = null, activeTab = $bindable('general') } = $props();

  let workspace = $state(null);
  let loading = $state(true);
  let saving = $state(false);
  let showDeleteConfirm = $state(false);
  let deleteConfirmText = $state('');
  let timeProjects = $state([]);
  let configurationRefreshKey = $state(0);
  // Bumped when the skills panel changes a skill so the bindings panel's
  // attach-pickers refresh (the two are siblings on the coding-agents tab).
  let agentSkillsVersion = $state(0);
  let creatingAgentBinding = $state(false);

  // Time project categories state
  let timeProjectCategories = $state([]);
  let selectedTimeProjectCategories = $state([]);

  let formData = $state({
    name: '',
    key: '',
    description: '',
    active: true,
    time_project_id: null,
    default_view: 'board',
    internal_comments_enabled: false
  });

  // The active admin module (registry-driven), used to render the page header.
  const currentModule = $derived(
    workspaceSettingsItems.find((m) => m.id === activeTab) || workspaceSettingsItems[0]
  );
  // Per-module page-header subtitle keys (id → i18n key).
  const HEADER_SUBTITLE = {
    general: 'workspaceSettings.headers.general',
    categories: 'workspaceSettings.headers.categories',
    members: 'workspaceSettings.headers.members',
    configuration: 'workspaceSettings.headers.configuration',
    'source-control': 'workspaceSettings.headers.sourceControl',
    'coding-agents': 'workspaceSettings.headers.codingAgents',
    'issue-sync': 'workspaceSettings.headers.issueSync',
    recurrence: 'workspaceSettings.headers.recurrence',
    templates: 'workspaceSettings.headers.templates',
    danger: 'workspaceSettings.headers.danger',
  };

  // Permission check for workspace admin
  const canAdmin = $derived(workspacePermissions.canAdminWorkspace(workspaceId));

  onMount(async () => {
    await moduleSettings.load();

    // Redirect from base settings route to general tab
    if (window.location.pathname === `/workspaces/${workspaceId}/settings`) {
      navigate(`/workspaces/${workspaceId}/settings/general`);
      // Don't return — still load data so the component isn't stuck in loading state
    }

    const loadPromises = [loadWorkspace(), loadTimeProjectCategories()];
    if ($moduleSettings.time_tracking_enabled) {
      loadPromises.push(loadTimeProjects());
    }

    await Promise.all(loadPromises);
    loading = false;
  });

  async function loadWorkspace() {
    try {
      workspace = await api.workspaces.get(workspaceId);
      if (workspace) {
        formData = {
          name: workspace.name,
          key: workspace.key || '',
          description: workspace.description || '',
          active: workspace.active,
          time_project_id: workspace.time_project_id || null,
          default_view: workspace.default_view || 'board',
          internal_comments_enabled: workspace.internal_comments_enabled || false
        };
        selectedTimeProjectCategories = workspace.time_project_categories || [];
      }
    } catch (error) {
      console.error('Failed to load workspace:', error);
    }
  }

  async function loadTimeProjects() {
    try {
      timeProjects = await api.time.projects.getAll() || [];
    } catch (error) {
      console.error('Failed to load time projects:', error);
      timeProjects = [];
    }
  }

  async function loadTimeProjectCategories() {
    try {
      timeProjectCategories = await api.time.projectCategories.getAll() || [];
    } catch (error) {
      console.error('Failed to load time project categories:', error);
      timeProjectCategories = [];
    }
  }

  async function saveWorkspace() {
    if (!formData.name.trim()) {
      errorToast(t('workspaceSettings.workspaceNameRequired'));
      return;
    }

    if (!formData.key.trim()) {
      errorToast(t('workspaceSettings.workspaceKeyRequired'));
      return;
    }

    try {
      saving = true;
      await api.workspaces.update(workspaceId, {
        ...formData,
        time_project_id: formData.time_project_id ? parseInt(formData.time_project_id, 10) : null,
        time_project_categories: selectedTimeProjectCategories
      });

      // Update local workspace object
      workspace = { ...workspace, ...formData };

      // Update stores so sidebar dropdown reflects name/description changes immediately
      workspacesStore.updateWorkspace(workspaceId, { name: formData.name, description: formData.description });
      currentWorkspace.patch({ name: formData.name, description: formData.description });

      successToast(t('workspaceSettings.savedSuccessfully'));
    } catch (error) {
      console.error('Failed to save workspace:', error);
      errorToast(t('workspaceSettings.failedToSave', { error: error.message || error }));
    } finally {
      saving = false;
    }
  }

  function cancelDeleteWorkspace() {
    showDeleteConfirm = false;
    deleteConfirmText = '';
  }

  async function deleteWorkspace() {
    if (deleteConfirmText !== workspace.name) {
      errorToast(t('workspaceSettings.pleaseConfirmDeletion'));
      return;
    }

    try {
      await api.workspaces.delete(workspaceId);
      successToast(t('workspaceSettings.deletedSuccessfully', { name: workspace.name }));
      setTimeout(() => {
        navigate('/workspaces');
      }, 1000);
    } catch (error) {
      console.error('Failed to delete workspace:', error);
      errorToast(t('workspaceSettings.failedToDelete', { error: error.message || error }));
    }
  }

  function handleConfigurationChanged() {
    configurationRefreshKey++;
  }
</script>

{#if loading}
  <Card rounded="xl" shadow padding="spacious">
    <div class="animate-pulse">
      <div class="h-4 rounded w-1/4 mb-4" style="background-color: var(--ds-surface);"></div>
      <div class="h-4 rounded w-3/4" style="background-color: var(--ds-surface);"></div>
    </div>
  </Card>
{:else if !canAdmin}
  <Card rounded="xl" shadow padding="loose">
    <div class="text-center py-8">
      <Shield class="w-12 h-12 mx-auto mb-4 text-amber-500" />
      <h2 class="text-lg font-semibold mb-2" style="color: var(--ds-text);">{t('workspaceSettings.accessDenied')}</h2>
      <p class="text-sm mb-4" style="color: var(--ds-text-subtle);">{t('workspaceSettings.accessDeniedDescription')}</p>
      <Button href={`/workspaces/${workspaceId}`} variant="primary">
        {t('workspaceSettings.backToWorkspace')}
      </Button>
    </div>
  </Card>
{:else if workspace}
  <div class="space-y-6">
    <PageHeader
      icon={currentModule?.icon}
      title={t(currentModule?.labelKey)}
      subtitle={t(HEADER_SUBTITLE[activeTab])}
    />

    <div class="workspace-settings-content">
      {#if activeTab === 'general'}
        <!-- Basic Information -->
        <h3 class="text-lg font-medium mb-6" style="color: var(--ds-text);">{t('workspaceSettings.basicInformation')}</h3>

        <div class="space-y-6">
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <Label for="workspace-name" required class="mb-2">{t('workspaceSettings.workspaceName')}</Label>
            <Input
              id="workspace-name"
              bind:value={formData.name}
              placeholder={t('workspaceSettings.workspaceNamePlaceholder')}
              required
            />
          </div>

          <div>
            <Label for="workspace-key" required class="mb-2">{t('workspaceSettings.workspaceKey')}</Label>
            <Input
              id="workspace-key"
              bind:value={formData.key}
              placeholder={t('workspaceSettings.workspaceKeyPlaceholder')}
              required
            />
            <DescriptionText>
              {t('workspaceSettings.workspaceKeyHelp')}
            </DescriptionText>
          </div>
        </div>

        <div>
          <Label for="workspace-description" class="mb-2">{t('workspaceSettings.description')}</Label>
          <Textarea
            id="workspace-description"
            bind:value={formData.description}
            rows={3}
            placeholder={t('workspaceSettings.descriptionPlaceholder')}
          />
        </div>

        {#if $moduleSettings.time_tracking_enabled}
          <div>
            <Label for="workspace-project" class="mb-2">{t('workspaceSettings.defaultTimeProject')}</Label>
            <Select
              id="workspace-project"
              bind:value={formData.time_project_id}
              options={[{ value: null, label: t('workspaceSettings.noDefaultProject') }, ...timeProjects.map(project => ({ value: project.id, label: `${project.name} (${project.customer_name})` }))]}
            />
            <DescriptionText>
              {t('workspaceSettings.defaultTimeProjectHelp')}
            </DescriptionText>
          </div>
        {/if}

        <div>
          <Label for="workspace-view" class="mb-2">{t('workspaceSettings.defaultView')}</Label>
          <Select
            id="workspace-view"
            bind:value={formData.default_view}
            options={[
              { value: 'board', label: t('workspaceSettings.views.board') },
              { value: 'backlog', label: t('workspaceSettings.views.backlog') },
              { value: 'list', label: t('workspaceSettings.views.list') },
              { value: 'tree', label: t('workspaceSettings.views.tree') },
              { value: 'map', label: t('workspaceSettings.views.map') },
              { value: 'overview', label: t('workspaceSettings.views.overview') },
            ]}
          />
          <DescriptionText>
            {t('workspaceSettings.defaultViewHelp')}
          </DescriptionText>
        </div>

        <div class="flex items-center justify-between">
          <div>
            <div class="text-sm font-medium mb-1" style="color: var(--ds-text);">
              {t('workspaceSettings.activeWorkspace')}
            </div>
            <p class="text-xs" style="color: var(--ds-text-subtle);">
              {t('workspaceSettings.activeWorkspaceHelp')}
            </p>
          </div>
          <Toggle bind:checked={formData.active} />
        </div>

        <div class="flex items-center justify-between">
          <div>
            <div class="text-sm font-medium mb-1" style="color: var(--ds-text);">
              {t('workspaceSettings.enableInternalComments')}
            </div>
            <p class="text-xs" style="color: var(--ds-text-subtle);">
              {t('workspaceSettings.enableInternalCommentsHint')}
            </p>
          </div>
          <Toggle bind:checked={formData.internal_comments_enabled} />
        </div>
        </div>

        <div class="flex items-center gap-3 mt-6">
        <Button
          variant="primary"
          size="medium"
          onclick={saveWorkspace}
          disabled={saving || !formData.name.trim() || !formData.key.trim()}
        >
          {#if saving}{t('workspaceSettings.saving')}{:else}{t('workspaceSettings.saveChanges')}{/if}
        </Button>
        <Button
          variant="secondary"
          size="medium"
          onclick={loadWorkspace}
        >
          {t('workspaceSettings.reset')}
        </Button>
      </div>
    {:else if activeTab === 'categories'}
        <!-- Project Category Restrictions -->
        <div class="flex items-center gap-3 mb-6">
          <Clock class="w-5 h-5" style="color: var(--ds-text-subtle);" />
          <h3 class="text-lg font-medium" style="color: var(--ds-text);">{t('workspaceSettings.projectCategoryRestrictions')}</h3>
        </div>

        <CategoryMultiSelect
          categories={timeProjectCategories}
          bind:selectedIds={selectedTimeProjectCategories}
          placeholder={t('workspaceSettings.selectProjectCategories')}
          helperText={t('workspaceSettings.categoryRestrictionsHelp')}
        />

        <p class="text-xs mt-3" style="color: var(--ds-text-subtle);">
          {t('workspaceSettings.leaveEmptyNote')}
        </p>

        <div class="flex items-center gap-3 mt-6">
          <Button
            variant="primary"
            size="medium"
            onclick={saveWorkspace}
            disabled={saving || !formData.name.trim() || !formData.key.trim()}
          >
            {#if saving}{t('workspaceSettings.saving')}{:else}{t('workspaceSettings.saveChanges')}{/if}
          </Button>
          <Button
            variant="secondary"
            size="medium"
            onclick={loadWorkspace}
          >
            {t('workspaceSettings.reset')}
          </Button>
        </div>
    {:else if activeTab === 'members'}
        <!-- Workspace Members -->
        <WorkspaceMembers {workspaceId} />
    {:else if activeTab === 'configuration'}
        <!-- Configuration Sets -->
        <WorkspaceConfigurationAssigner workspaceId={workspaceId} onconfigurationChanged={handleConfigurationChanged} />

        <!-- Active Configuration Preview -->
        <div class="mt-6 pt-6 border-t" style="border-color: var(--ds-border);">
          <h3 class="text-lg font-medium mb-4" style="color: var(--ds-text);">{t('workspaceSettings.activeConfiguration')}</h3>
          {#key configurationRefreshKey}
            <WorkspaceConfigurationPreview {workspaceId} />
          {/key}
        </div>

    {:else if activeTab === 'source-control'}
        <!-- Source Control Settings -->
        <WorkspaceSCMSettings {workspaceId} />

    {:else if activeTab === 'coding-agents'}
        <!-- Coding Agent Bindings (WI-88) + skills library (WI-258) -->
        <div class="space-y-4">
            <WorkspaceAgentBindings
              {workspaceId}
              skillsVersion={agentSkillsVersion}
              oncreatingchange={(creating) => (creatingAgentBinding = creating)}
            />
            {#if !creatingAgentBinding}
              <WorkspaceAgentSkills {workspaceId} onchanged={() => (agentSkillsVersion += 1)} />
            {/if}
        </div>

    {:else if activeTab === 'issue-sync'}
        <!-- Issue Sync Settings -->
        <IssueSyncSettings {workspaceId} />

    {:else if activeTab === 'recurrence'}
        <!-- Recurrence Rules -->
        <RecurrenceManager {workspaceId} />

    {:else if activeTab === 'templates'}
        <!-- Work item templates (WI-438) -->
        <WorkspaceItemTemplates {workspaceId} />

    {:else if activeTab === 'danger'}
        <!-- Remove Workspace -->
        <div class="flex items-center gap-3 mb-6">
          <AlertTriangle class="w-5 h-5 text-red-600" />
          <h3 class="text-lg font-medium text-red-900">{t('workspaceSettings.permanentRemoval')}</h3>
        </div>

        <div class="text-sm text-red-700 mb-6">
          <p class="mb-4">{t('workspaceSettings.removeWarningIntro')}</p>
          <ul class="list-disc list-inside space-y-2 ml-4">
            <li>{t('workspaceSettings.removeWarningItems')}</li>
            <li>{t('workspaceSettings.removeWarningFields')}</li>
            <li>{t('workspaceSettings.removeWarningScreens')}</li>
            <li>{t('workspaceSettings.removeWarningFiles')}</li>
          </ul>
          <p class="mt-4 font-medium">{t('workspaceSettings.removeWarningFinal')}</p>
        </div>

        {#if !showDeleteConfirm}
          <button
            onclick={() => showDeleteConfirm = true}
            class="flex items-center gap-2 px-4 py-2 bg-red-600 text-white text-sm font-medium rounded hover:bg-red-700 transition-colors"
          >
            <Trash2 class="w-4 h-4" />
            {t('workspaceSettings.removeWorkspaceButton')}
          </button>
        {:else}
          <div class="space-y-4">
            <div>
              <label for="delete-confirm" class="block text-sm font-medium text-red-900 mb-2">
                {t('workspaceSettings.typeToConfirm', { name: workspace.name })}
              </label>
              <input
                id="delete-confirm"
                type="text"
                bind:value={deleteConfirmText}
                class="w-full px-4 py-2 rounded border border-red-300 text-red-900 bg-white focus:outline-none focus:ring-2 focus:ring-red-500"
                placeholder={t('workspaceSettings.typeNameHere', { name: workspace.name })}
              />
            </div>

            <div class="flex items-center gap-3">
              <button
                onclick={deleteWorkspace}
                disabled={deleteConfirmText !== workspace.name}
                class="px-4 py-2 bg-red-600 text-white text-sm font-medium rounded hover:bg-red-700 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {t('workspaceSettings.yesRemoveWorkspace')}
              </button>
              <button
                onclick={cancelDeleteWorkspace}
                data-testid="cancel-delete-workspace"
                class="px-4 py-2 text-sm font-medium rounded border transition-colors hover-danger" style="border-color: var(--ds-border-danger); color: var(--ds-text-danger);"
              >
                {t('workspaceSettings.cancel')}
              </button>
            </div>
          </div>
        {/if}
    {/if}
    </div>

  </div>
{:else}
  <div class="rounded-xl p-6 border shadow-sm" style="background-color: var(--ds-surface-raised); border-color: var(--ds-border);">
    <p class="text-center" style="color: var(--ds-text-subtle);">{t('workspaceSettings.workspaceNotFound')}</p>
  </div>
{/if}
