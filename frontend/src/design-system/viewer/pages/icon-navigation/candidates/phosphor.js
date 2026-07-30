import BellIcon from 'phosphor-svelte/lib/BellIcon';
import BookIcon from 'phosphor-svelte/lib/BookIcon';
import CalendarIcon from 'phosphor-svelte/lib/CalendarIcon';
import ChatIcon from 'phosphor-svelte/lib/ChatIcon';
import ClockIcon from 'phosphor-svelte/lib/ClockIcon';
import DatabaseIcon from 'phosphor-svelte/lib/DatabaseIcon';
import DesktopIcon from 'phosphor-svelte/lib/DesktopIcon';
import EnvelopeIcon from 'phosphor-svelte/lib/EnvelopeIcon';
import FileTextIcon from 'phosphor-svelte/lib/FileTextIcon';
import FlagIcon from 'phosphor-svelte/lib/FlagIcon';
import FlowArrowIcon from 'phosphor-svelte/lib/FlowArrowIcon';
import FolderSimpleIcon from 'phosphor-svelte/lib/FolderSimpleIcon';
import GearIcon from 'phosphor-svelte/lib/GearIcon';
import GitMergeIcon from 'phosphor-svelte/lib/GitMergeIcon';
import LifebuoyIcon from 'phosphor-svelte/lib/LifebuoyIcon';
import LinkIcon from 'phosphor-svelte/lib/LinkIcon';
import MagnifyingGlassIcon from 'phosphor-svelte/lib/MagnifyingGlassIcon';
import PackageIcon from 'phosphor-svelte/lib/PackageIcon';
import PaletteIcon from 'phosphor-svelte/lib/PaletteIcon';
import PaperclipIcon from 'phosphor-svelte/lib/PaperclipIcon';
import PhoneCallIcon from 'phosphor-svelte/lib/PhoneCallIcon';
import PlugsIcon from 'phosphor-svelte/lib/PlugsIcon';
import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
import PulseIcon from 'phosphor-svelte/lib/PulseIcon';
import RobotIcon from 'phosphor-svelte/lib/RobotIcon';
import ShieldIcon from 'phosphor-svelte/lib/ShieldIcon';
import SquaresFourIcon from 'phosphor-svelte/lib/SquaresFourIcon';
import StampIcon from 'phosphor-svelte/lib/StampIcon';
import TreeStructureIcon from 'phosphor-svelte/lib/TreeStructureIcon';
import UsersThreeIcon from 'phosphor-svelte/lib/UsersThreeIcon';
import WarningIcon from 'phosphor-svelte/lib/WarningIcon';

export default {
  id: 'phosphor',
  label: 'Phosphor',
  license: 'MIT',
  mode: 'phosphor',
  description: 'Regular Phosphor glyphs with expressive filled selected states.',
  mainSize: 20,
  adminSize: 16,
  icons: {
    workspace: SquaresFourIcon,
    collections: FolderSimpleIcon,
    time: ClockIcon,
    milestones: FlagIcon,
    iterations: CalendarIcon,
    knowledge: BookIcon,
    assets: PackageIcon,
    channels: LifebuoyIcon,
    portal: ChatIcon,
    organizations: UsersThreeIcon,
    teams: PhoneCallIcon,
    create: PlusIcon,
    search: MagnifyingGlassIcon,
    settings: GearIcon,
    notifications: BellIcon,
    customFields: DatabaseIcon,
    screens: DesktopIcon,
    hierarchy: TreeStructureIcon,
    itemTypes: FileTextIcon,
    priorities: WarningIcon,
    statuses: FlowArrowIcon,
    workflows: FlowArrowIcon,
    approvals: StampIcon,
    ai: RobotIcon,
    scm: GitMergeIcon,
    integrations: PlugsIcon,
    links: LinkIcon,
    attachments: PaperclipIcon,
    themes: PaletteIcon,
    mail: EnvelopeIcon,
    security: ShieldIcon,
    activity: PulseIcon,
  },
};
