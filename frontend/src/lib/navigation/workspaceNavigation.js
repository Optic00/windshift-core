import { GanttChart } from '@lucide/svelte';
import {
  IconChartBar as BarChart3,
  IconCalendar as Calendar,
  IconFileCheck as FileCheck,
  IconFileStack as FileStack,
  IconList as List,
  IconListTree as ListTree,
  IconMapPin as MapPin,
  IconFlag as Milestone,
  IconPackage as Package,
  IconPlayerPlay as Play,
  IconLayoutRows as Rows_3,
  IconLayoutKanban as SquareKanban,
  IconTrendingUp as TrendingUp,
  IconBolt as Zap,
} from '@tabler/icons-svelte-runes';

/**
 * @typedef {Object} WorkspaceView
 * @property {string} id
 * @property {string} label
 * @property {any}    icon
 * @property {string} [tooltip]
 * @property {string[]} [activeViews]  Route view names that highlight this item.
 */

/**
 * Collection-scoped workspace views (visible inside collections too).
 * @type {WorkspaceView[]}
 */
export const workspaceViewItems = [
  { id: 'backlog', label: 'Backlog', icon: Rows_3, tooltip: 'Backlog view for unfinished items' },
  { id: 'board', label: 'Board', icon: SquareKanban, tooltip: 'Kanban board view with columns' },
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
 * Workspace-only views (hidden inside collections).
 * @type {WorkspaceView[]}
 */
export const workspaceOnlyViews = [
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
