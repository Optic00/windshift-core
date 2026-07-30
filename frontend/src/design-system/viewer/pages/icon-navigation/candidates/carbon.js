import Activity from 'carbon-icons-svelte/lib/Activity.svelte';
import Add from 'carbon-icons-svelte/lib/Add.svelte';
import Attachment from 'carbon-icons-svelte/lib/Attachment.svelte';
import Book from 'carbon-icons-svelte/lib/Book.svelte';
import Calendar from 'carbon-icons-svelte/lib/Calendar.svelte';
import Chat from 'carbon-icons-svelte/lib/Chat.svelte';
import DataBase from 'carbon-icons-svelte/lib/DataBase.svelte';
import DecisionTree from 'carbon-icons-svelte/lib/DecisionTree.svelte';
import Document from 'carbon-icons-svelte/lib/Document.svelte';
import Email from 'carbon-icons-svelte/lib/Email.svelte';
import Flag from 'carbon-icons-svelte/lib/Flag.svelte';
import Flow from 'carbon-icons-svelte/lib/Flow.svelte';
import Folder from 'carbon-icons-svelte/lib/Folder.svelte';
import Grid from 'carbon-icons-svelte/lib/Grid.svelte';
import Group from 'carbon-icons-svelte/lib/Group.svelte';
import HelpDesk from 'carbon-icons-svelte/lib/HelpDesk.svelte';
import Link from 'carbon-icons-svelte/lib/Link.svelte';
import MachineLearning from 'carbon-icons-svelte/lib/MachineLearning.svelte';
import Merge from 'carbon-icons-svelte/lib/Merge.svelte';
import Notification from 'carbon-icons-svelte/lib/Notification.svelte';
import Package from 'carbon-icons-svelte/lib/Package.svelte';
import PaintBrush from 'carbon-icons-svelte/lib/PaintBrush.svelte';
import PhoneVoice from 'carbon-icons-svelte/lib/PhoneVoice.svelte';
import Plug from 'carbon-icons-svelte/lib/Plug.svelte';
import Screen from 'carbon-icons-svelte/lib/Screen.svelte';
import Search from 'carbon-icons-svelte/lib/Search.svelte';
import Security from 'carbon-icons-svelte/lib/Security.svelte';
import Settings from 'carbon-icons-svelte/lib/Settings.svelte';
import Stamp from 'carbon-icons-svelte/lib/Stamp.svelte';
import Time from 'carbon-icons-svelte/lib/Time.svelte';
import TreeView from 'carbon-icons-svelte/lib/TreeView.svelte';
import Warning from 'carbon-icons-svelte/lib/Warning.svelte';

export default {
  id: 'carbon',
  label: 'Carbon',
  license: 'Apache-2.0',
  mode: 'carbon',
  description: 'Productive 16/20px Carbon glyphs with a compact, industrial enterprise character.',
  mainSize: 20,
  adminSize: 16,
  icons: {
    workspace: Grid,
    collections: Folder,
    time: Time,
    milestones: Flag,
    iterations: Calendar,
    knowledge: Book,
    assets: Package,
    channels: HelpDesk,
    portal: Chat,
    organizations: Group,
    teams: PhoneVoice,
    create: Add,
    search: Search,
    settings: Settings,
    notifications: Notification,
    customFields: DataBase,
    screens: Screen,
    hierarchy: TreeView,
    itemTypes: Document,
    priorities: Warning,
    statuses: Flow,
    workflows: DecisionTree,
    approvals: Stamp,
    ai: MachineLearning,
    scm: Merge,
    integrations: Plug,
    links: Link,
    attachments: Attachment,
    themes: PaintBrush,
    mail: Email,
    security: Security,
    activity: Activity,
  },
};
