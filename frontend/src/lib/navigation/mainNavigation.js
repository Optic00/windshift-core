import {
  IconBook,
  IconCalendar,
  IconClock,
  IconFlag,
  IconFolderSearch,
  IconLifebuoy,
  IconMessage2Plus,
  IconPackage,
  IconPhoneCheck,
  IconSettings,
  IconUsers,
} from '@tabler/icons-svelte-runes';

/**
 * @typedef {Object} NavItem
 * @property {string} id
 * @property {any} icon
 * @property {string} labelKey   i18n key resolved at render time via t().
 * @property {string} href
 * @property {string[]} activeViews  Route view names that highlight this item.
 * @property {string} [permission]   Key on permissionStore that must be truthy to show.
 */

/** @type {NavItem[]} */
export const mainNavItems = [
  {
    id: 'collections',
    icon: IconFolderSearch,
    labelKey: 'nav.collections',
    href: '/collections',
    activeViews: ['collections-list'],
  },
  {
    id: 'time',
    icon: IconClock,
    labelKey: 'nav.timeAndProjects',
    href: '/time',
    activeViews: ['time'],
  },
  {
    id: 'milestones',
    icon: IconFlag,
    labelKey: 'nav.milestones',
    href: '/milestones',
    activeViews: ['milestones', 'milestone-detail'],
  },
  {
    id: 'iterations',
    icon: IconCalendar,
    labelKey: 'nav.iterations',
    href: '/iterations',
    activeViews: ['iterations', 'iteration-detail'],
  },
  {
    id: 'logbook',
    icon: IconBook,
    labelKey: 'nav.knowledgeBase',
    href: '/logbook',
    activeViews: ['logbook', 'logbook-document'],
    permission: 'canAccessLogbook',
  },
  {
    id: 'assets',
    icon: IconPackage,
    labelKey: 'nav.assets',
    href: '/assets',
    activeViews: ['assets', 'asset-detail'],
    permission: 'canAccessAssets',
  },
  {
    id: 'channel-management',
    icon: IconLifebuoy,
    labelKey: 'nav.channels',
    href: '/manage/channels',
    activeViews: ['channel-manager'],
    permission: 'canManageChannels',
  },
  {
    id: 'portal-hub',
    icon: IconMessage2Plus,
    labelKey: 'nav.portalHub',
    href: '/channels',
    activeViews: ['hub', 'hub-inbox', 'channels'],
    permission: 'canAccessPortalHub',
  },
  {
    id: 'organizations',
    icon: IconUsers,
    labelKey: 'nav.organizations',
    href: '/organizations',
    activeViews: ['organizations'],
    permission: 'canAccessCustomers',
  },
  {
    id: 'teams',
    icon: IconPhoneCheck,
    labelKey: 'nav.teams',
    href: '/teams',
    activeViews: ['teams-list', 'team-detail'],
  },
];

/** @type {NavItem[]} */
export const bottomNavItems = [
  {
    id: 'admin',
    icon: IconSettings,
    labelKey: 'nav.admin',
    href: '/admin',
    activeViews: ['admin'],
    permission: 'canAccessAdmin',
  },
];
