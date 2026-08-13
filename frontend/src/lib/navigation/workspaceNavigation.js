import { GanttChart } from '@lucide/svelte';
import {
  IconAdjustments as Adjustments,
  IconUserStar as AgentIcon,
  IconChartBar as BarChart3,
  IconBook as Book,
  IconCalendar as Calendar,
  IconFileCheck as FileCheck,
  IconFileStack as FileStack,
  IconGitBranch as GitBranch,
  IconList as List,
  IconListTree as ListTree,
  IconMapPin as MapPin,
  IconFlag as Milestone,
  IconPackage as Package,
  IconPlayerPlay as Play,
  IconRefresh as Refresh,
  IconRepeat as Repeat,
  IconLayoutRows as Rows_3,
  IconSettings as SettingsCog,
  IconLayoutKanban as SquareKanban,
  IconTags as Tags,
  IconTrash as Trash,
  IconTrendingUp as TrendingUp,
  IconUsers as Users,
  IconBolt as Zap,
} from '@tabler/icons-svelte-runes';

/**
 * @typedef {Object} WorkspaceView
 * @property {string} id
 * @property {string} label
 * @property {any}    icon
 * @property {string} [tooltip]
 * @property {string} [testId]
 * @property {string[]} [activeViews]  Route view names that highlight this item.
 */

/**
 * Collection-scoped workspace views (visible inside collections too).
 * @type {WorkspaceView[]}
 */
export const workspaceViewItems = [
  { id: 'backlog', label: 'Backlog', icon: Rows_3, tooltip: 'Backlog view for unfinished items' },
  {
    id: 'board',
    label: 'Board',
    icon: SquareKanban,
    tooltip: 'Kanban board view with columns',
    testId: 'workspace-nav-board',
  },
  { id: 'list', label: 'List', icon: List, tooltip: 'Detailed list view with all fields' },
  { id: 'tree', label: 'Tree', icon: ListTree, tooltip: 'Hierarchical tree view for nested items' },
  { id: 'map', label: 'Map', icon: MapPin, tooltip: 'Visual map view for spatial organization' },
  {
    id: 'roadmap',
    label: 'Roadmap',
    icon: GanttChart,
    tooltip: 'Timeline view with date ranges and dependencies',
  },
];

/**
 * Workspace tools which are not scoped to a collection.
 * @type {WorkspaceView[]}
 */
export const workspaceOnlyViews = [
  {
    id: 'agents',
    label: 'Agents',
    icon: AgentIcon,
    tooltip: 'Meet and work with workspace agents',
    testId: 'workspace-nav-agents',
    activeViews: ['workspace-agents', 'workspace-agent-profile', 'workspace-agent-create'],
  },
  {
    id: 'iterations',
    label: 'Iterations',
    icon: Calendar,
    tooltip: 'Manage sprints, PIs, and other iteration cycles',
  },
  {
    id: 'milestones',
    label: 'Milestones',
    icon: Milestone,
    tooltip: 'Manage workspace milestones and releases',
  },
  {
    id: 'analytics',
    label: 'Analytics',
    icon: TrendingUp,
    tooltip: 'Velocity, cycle time, and forecasting',
  },
  { id: 'actions', label: 'Actions', icon: Zap, tooltip: 'Automate workflows and triggers' },
  {
    id: 'pages',
    label: 'Pages',
    icon: Book,
    tooltip: 'Workspace knowledge pages (wiki)',
    activeViews: ['workspace-pages'],
  },
];

/**
 * Test management navigation items, visible only when the test-management
 * module is enabled AND the user has view permission.
 * @type {WorkspaceView[]}
 */
export const testNavigationItems = [
  {
    id: 'test-cases',
    label: 'Test Cases',
    icon: FileCheck,
    tooltip: 'Manage test cases and steps',
    activeViews: ['test-cases', 'test-case-detail', 'test-steps'],
  },
  {
    id: 'test-sets',
    label: 'Test Plans',
    icon: Package,
    tooltip: 'Organize plans and suites',
    activeViews: ['test-sets', 'test-set-detail'],
  },
  {
    id: 'test-templates',
    label: 'Templates',
    icon: FileStack,
    tooltip: 'Template runs and shared steps',
    activeViews: ['test-templates', 'test-template-detail'],
  },
  {
    id: 'test-runs',
    label: 'Test Runs',
    icon: Play,
    tooltip: 'Schedule and execute runs',
    activeViews: ['test-runs', 'test-run-detail', 'test-execution'],
  },
  {
    id: 'test-reports',
    label: 'Reports',
    icon: BarChart3,
    tooltip: 'Review execution results',
    activeViews: ['test-reports'],
  },
];

/**
 * @typedef {Object} WorkspaceSettingsItem
 * @property {string}  id        Module id (matches the `/settings/<id>` route segment).
 * @property {string}  labelKey  i18n key for the module label (resolve with `t()`).
 * @property {any}     icon      Icon component.
 * @property {string}  view      Route view name that highlights this item.
 * @property {boolean} [danger]  Styled as a destructive action when true.
 */

/**
 * Workspace admin (Settings) modules, in display order. Drives the folded
 * admin sidebar (`WorkspaceAdminNav`) and its collapsed icon rail. Routes are
 * `/workspaces/:id/settings/<id>` — use `workspaceSettingsRoute(workspaceId, id)`.
 * @type {WorkspaceSettingsItem[]}
 */
export const workspaceSettingsItems = [
  {
    id: 'general',
    labelKey: 'workspaceSettings.tabs.general',
    icon: SettingsCog,
    view: 'workspace-settings-general',
  },
  {
    id: 'categories',
    labelKey: 'workspaceSettings.tabs.categories',
    icon: Tags,
    view: 'workspace-settings-categories',
  },
  {
    id: 'members',
    labelKey: 'workspaceSettings.tabs.members',
    icon: Users,
    view: 'workspace-settings-members',
  },
  {
    id: 'configuration',
    labelKey: 'workspaceSettings.tabs.configurationSets',
    icon: Adjustments,
    view: 'workspace-settings-configuration',
  },
  {
    id: 'source-control',
    labelKey: 'workspaceSettings.tabs.sourceControl',
    icon: GitBranch,
    view: 'workspace-settings-source-control',
  },
  {
    id: 'issue-sync',
    labelKey: 'workspaceSettings.tabs.issueSync',
    icon: Refresh,
    view: 'workspace-settings-issue-sync',
  },
  {
    id: 'recurrence',
    labelKey: 'workspaceSettings.tabs.recurrence',
    icon: Repeat,
    view: 'workspace-settings-recurrence',
  },
  {
    id: 'templates',
    labelKey: 'workspaceSettings.tabs.templates',
    icon: FileStack,
    view: 'workspace-settings-templates',
  },
  {
    id: 'danger',
    labelKey: 'workspaceSettings.tabs.removeWorkspace',
    icon: Trash,
    view: 'workspace-settings-danger',
    danger: true,
  },
];

/** All route view names that belong to the workspace admin area. */
export const workspaceSettingsViews = [
  'workspace-settings',
  ...workspaceSettingsItems.map((item) => item.view),
];

/** Build the route for a settings module. */
export function workspaceSettingsRoute(workspaceId, id) {
  return `/workspaces/${workspaceId}/settings/${id}`;
}
