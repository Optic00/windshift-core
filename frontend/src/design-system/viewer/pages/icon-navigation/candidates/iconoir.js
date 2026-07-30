import activity from 'iconoir/icons/regular/activity.svg?raw';
import attachment from 'iconoir/icons/regular/attachment.svg?raw';
import bellNotification from 'iconoir/icons/regular/bell-notification.svg?raw';
import book from 'iconoir/icons/regular/book.svg?raw';
import brainElectricity from 'iconoir/icons/regular/brain-electricity.svg?raw';
import calendar from 'iconoir/icons/regular/calendar.svg?raw';
import chatBubble from 'iconoir/icons/regular/chat-bubble.svg?raw';
import checkCircle from 'iconoir/icons/regular/check-circle.svg?raw';
import community from 'iconoir/icons/regular/community.svg?raw';
import database from 'iconoir/icons/regular/database.svg?raw';
import folder from 'iconoir/icons/regular/folder.svg?raw';
import gitBranch from 'iconoir/icons/regular/git-branch.svg?raw';
import gitMerge from 'iconoir/icons/regular/git-merge.svg?raw';
import headsetHelp from 'iconoir/icons/regular/headset-help.svg?raw';
import link from 'iconoir/icons/regular/link.svg?raw';
import mail from 'iconoir/icons/regular/mail.svg?raw';
import packageIcon from 'iconoir/icons/regular/package.svg?raw';
import page from 'iconoir/icons/regular/page.svg?raw';
import palette from 'iconoir/icons/regular/palette.svg?raw';
import pathArrow from 'iconoir/icons/regular/path-arrow.svg?raw';
import phone from 'iconoir/icons/regular/phone.svg?raw';
import plusSquare from 'iconoir/icons/regular/plus-square.svg?raw';
import search from 'iconoir/icons/regular/search.svg?raw';
import settings from 'iconoir/icons/regular/settings.svg?raw';
import shield from 'iconoir/icons/regular/shield.svg?raw';
import tree from 'iconoir/icons/regular/tree.svg?raw';
import triangleFlag from 'iconoir/icons/regular/triangle-flag.svg?raw';
import viewGrid from 'iconoir/icons/regular/view-grid.svg?raw';
import warningTriangle from 'iconoir/icons/regular/warning-triangle.svg?raw';
import checkCircleSolid from 'iconoir/icons/solid/check-circle.svg?raw';
import databaseSolid from 'iconoir/icons/solid/database.svg?raw';
import warningTriangleSolid from 'iconoir/icons/solid/warning-triangle.svg?raw';

export default {
  id: 'iconoir',
  label: 'Iconoir',
  license: 'MIT',
  mode: 'iconoir',
  description:
    'Airy 1.5px Iconoir glyphs, with available solid glyphs used for selected navigation states.',
  mainSize: 20,
  adminSize: 16,
  icons: {
    workspace: viewGrid,
    collections: folder,
    time: activity,
    milestones: triangleFlag,
    iterations: calendar,
    knowledge: book,
    assets: packageIcon,
    channels: headsetHelp,
    portal: chatBubble,
    organizations: community,
    teams: phone,
    create: plusSquare,
    search,
    settings,
    notifications: bellNotification,
    customFields: database,
    screens: page,
    hierarchy: tree,
    itemTypes: page,
    priorities: warningTriangle,
    statuses: gitBranch,
    workflows: pathArrow,
    approvals: checkCircle,
    ai: brainElectricity,
    scm: gitMerge,
    integrations: settings,
    links: link,
    attachments: attachment,
    themes: palette,
    mail,
    security: shield,
    activity,
  },
  selectedIcons: {
    customFields: databaseSolid,
    priorities: warningTriangleSolid,
    approvals: checkCircleSolid,
  },
};
