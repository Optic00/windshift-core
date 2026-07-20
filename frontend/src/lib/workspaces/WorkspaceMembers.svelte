<script>
  import { onMount } from 'svelte';
  import { api } from '../api.js';
  import { UserPlus, Trash2, Shield, Users } from '@lucide/svelte';
  import Button from '../components/Button.svelte';
  import Modal from '../dialogs/Modal.svelte';
  import Select from '../components/Select.svelte';
  import UserPicker from '../pickers/UserPicker.svelte';
  import GroupPicker from '../pickers/GroupPicker.svelte';
  import DataTable from '../components/DataTable.svelte';
  import SearchInput from '../components/SearchInput.svelte';
  import Pagination from '../components/Pagination.svelte';
  import Avatar from '../components/Avatar.svelte';
  import Chip from '../components/Chip.svelte';
  import Text from '../components/Text.svelte';
  import Label from '../components/Label.svelte';
  import DescriptionText from '../components/DescriptionText.svelte';
  import { confirm } from '../composables/useConfirm.js';
  import { errorToast } from '../stores/toasts.svelte.js';
  import { toHotkeyString } from '../utils/keyboardShortcuts.js';

  let { workspaceId } = $props();

  let members = $state([]);
  let groupAssignments = $state([]);
  let roles = $state([]);
  let loading = $state(true);
  let error = $state(null);
  const defaultRoleOrder = ['Viewer', 'Editor', 'Tester', 'Administrator'];

  // Add member modal state
  let showModal = $state(false);
  let selectedUserId = $state(null);
  let selectedRoleId = $state(null);
  let adding = $state(false);

  // Add group modal state
  let showGroupModal = $state(false);
  let selectedGroupId = $state(null);
  let selectedGroupRoleId = $state(null);
  let addingGroup = $state(false);

  // Search and pagination state
  let searchQuery = $state('');
  let currentPage = $state(1);
  let itemsPerPage = $state(20);

  onMount(async () => {
    await loadData();
  });

  async function loadData() {
    const workspaceIdNum = Number(workspaceId);
    if (!workspaceIdNum) {
      error = 'Invalid workspace';
      return;
    }
    loading = true;
    error = null;
    try {
      const [membersData, groupsData, rolesData] = await Promise.all([
        api.workspaceRoles.getWorkspaceAssignments(workspaceIdNum),
        api.workspaceRoles.getWorkspaceGroupAssignments(workspaceIdNum),
        api.workspaceRoles.getAll()
      ]);
      members = membersData || [];
      groupAssignments = groupsData || [];
      roles = rolesData || [];
    } catch (err) {
      console.error('Failed to load workspace members:', err);
      error = err.message || 'Failed to load workspace members';
    } finally {
      loading = false;
    }
  }

  async function handleSubmit() {
    const roleId = selectedRoleId ? Number(selectedRoleId) : null;
    const userId = selectedUserId ? Number(selectedUserId) : null;
    const workspaceIdNum = Number(workspaceId);
    if (!userId || !roleId) {
      return;
    }

    try {
      adding = true;
      await api.workspaceRoles.assignToUser({
        user_id: userId,
        workspace_id: workspaceIdNum,
        role_id: roleId
      });

      // Reset form
      selectedUserId = null;
      selectedRoleId = null;
      showModal = false;

      // Reload data
      await loadData();
    } catch (err) {
      console.error('Failed to add member:', err);
      errorToast(`Failed to add member: ${err.message}`);
    } finally {
      adding = false;
    }
  }

  async function handleRemoveMemberRole(member, role) {
    const userName = `${member.first_name || ''} ${member.last_name || ''}`.trim() || member.username;
    const confirmed = await confirm({
      title: `Remove ${role.role_name} role from ${userName}?`,
      message: 'This will revoke this role assignment from the user.'
    });

    if (!confirmed) return;

    try {
      const workspaceIdNum = Number(workspaceId);
      await api.workspaceRoles.revokeFromUser(member.user_id, workspaceIdNum, role.role_id);

      // Reload data
      await loadData();
    } catch (err) {
      console.error('Failed to remove role:', err);
      errorToast(`Failed to remove role: ${err.message}`);
    }
  }

  async function handleSubmitGroup() {
    const roleId = selectedGroupRoleId ? Number(selectedGroupRoleId) : null;
    const groupId = selectedGroupId ? Number(selectedGroupId) : null;
    const workspaceIdNum = Number(workspaceId);
    if (!groupId || !roleId) {
      return;
    }

    try {
      addingGroup = true;
      await api.workspaceRoles.assignToGroup({
        group_id: groupId,
        workspace_id: workspaceIdNum,
        role_id: roleId
      });

      selectedGroupId = null;
      selectedGroupRoleId = null;
      showGroupModal = false;

      await loadData();
    } catch (err) {
      console.error('Failed to add group:', err);
      errorToast(`Failed to add group: ${err.message}`);
    } finally {
      addingGroup = false;
    }
  }

  async function handleRemoveGroupRole(group, role) {
    const confirmed = await confirm({
      title: `Remove ${role.role_name} role from ${group.group_name}?`,
      message: 'This will revoke this role assignment from the group.'
    });

    if (!confirmed) return;

    try {
      const workspaceIdNum = Number(workspaceId);
      await api.workspaceRoles.revokeFromGroup(group.group_id, workspaceIdNum, role.role_id);
      await loadData();
    } catch (err) {
      console.error('Failed to remove group role:', err);
      errorToast(`Failed to remove group role: ${err.message}`);
    }
  }

  function openGroupModal() {
    showGroupModal = true;
  }

  function canOpenGroupModal() {
    return !showGroupModal;
  }

  function handleCancelGroup() {
    showGroupModal = false;
    selectedGroupId = null;
    selectedGroupRoleId = null;
  }

  function getRoleBadgeStyle(roleId) {
    const role = roles.find(r => r.id === roleId);
    if (!role) return 'background-color: var(--ds-background-neutral); color: var(--ds-text-subtle);';

    // Role-specific styling using design system variables
    if (role.name === 'Administrator') {
      return 'background-color: var(--ds-background-accent-purple-subtler); color: var(--ds-accent-purple);';
    } else if (role.name === 'Editor') {
      return 'background-color: var(--ds-accent-blue-subtler); color: var(--ds-accent-blue);';
    } else if (role.name === 'Viewer') {
      return 'background-color: var(--ds-background-accent-green-subtler); color: var(--ds-accent-green);';
    } else if (role.name === 'Tester') {
      return 'background-color: var(--ds-accent-teal-subtler); color: var(--ds-accent-teal);';
    }
    return 'background-color: var(--ds-background-neutral); color: var(--ds-text-subtle);';
  }

  function handleCancel() {
    showModal = false;
    selectedUserId = null;
    selectedRoleId = null;
  }

  // Derive effective access from member assignments using the hierarchy:
  // Viewer -> Editor -> Tester. Admin always requires explicit assignment.
  // A role with no explicit members means "Everyone" has that access,
  // but only if the parent role in the hierarchy is also open.
  function getEffectiveAccess(roleName, roleMembers) {
    // A role is "open to everyone" only when it has NO explicit assignment —
    // neither a user member nor a group. This mirrors the backend, which counts
    // user_workspace_roles + group_workspace_roles when deciding everyone-access.
    const assignedCount = (name) =>
      members.filter(m => m.roles.some(r => r.role_name === name)).length +
      groupAssignments.filter(g => g.roles.some(r => r.role_name === name)).length;

    const viewerOpen = assignedCount('Viewer') === 0;
    const editorOpen = assignedCount('Editor') === 0;
    const testerOpen = assignedCount('Tester') === 0;

    const roleGroups = groupAssignments.filter(g => g.roles.some(r => r.role_name === roleName));
    const explicitCount = roleMembers.length + roleGroups.length;
    if (explicitCount > 0) {
      return { type: 'members', count: explicitCount };
    }

    if (roleName === 'Viewer') {
      return viewerOpen ? { type: 'everyone' } : { type: 'none' };
    }
    if (roleName === 'Editor') {
      return viewerOpen && editorOpen ? { type: 'everyone' } : { type: 'none' };
    }
    if (roleName === 'Tester') {
      return viewerOpen && editorOpen && testerOpen ? { type: 'everyone' } : { type: 'none' };
    }
    // Administrator: never implicit
    return { type: 'none' };
  }

  // DataTable columns
  const columns = [
    {
      key: 'principal',
      label: 'Member or group',
      slot: 'principal'
    },
    {
      key: 'roles',
      label: 'Roles',
      slot: 'role'
    },
    {
      key: 'actions',
      label: 'Actions',
      align: 'text-right'
    }
  ];

  function getAssignmentActionItems(assignment) {
    return assignment.roles.map(role => ({
      title: `Remove ${role.role_name}`,
      icon: Trash2,
      onClick: () => assignment.type === 'group'
        ? handleRemoveGroupRole(assignment, role)
        : handleRemoveMemberRole(assignment, role),
      hoverClass: 'hover-danger',
      iconClass: 'text-red-500'
    }));
  }

  const assignmentRows = $derived.by(() => {
    const userRows = members.map(member => ({
      ...member,
      type: 'user',
      row_key: `user-${member.user_id}`,
      displayName: `${member.first_name || ''} ${member.last_name || ''}`.trim() || member.username || member.email || 'Unknown user',
      detail: member.email || member.username || ''
    }));

    const groupRows = groupAssignments.map(group => ({
      ...group,
      type: 'group',
      row_key: `group-${group.group_id}`,
      displayName: group.group_name,
      detail: group.group_description || 'Group'
    }));

    return [...userRows, ...groupRows].sort((a, b) =>
      (a.displayName || '').localeCompare(b.displayName || '', undefined, { sensitivity: 'base' })
    );
  });

  // Search filtering
  let filteredAssignments = $derived(assignmentRows.filter(assignment => {
    if (!searchQuery.trim()) return true;

    const query = searchQuery.toLowerCase();
    return (
      assignment.displayName?.toLowerCase().includes(query) ||
      assignment.detail?.toLowerCase().includes(query) ||
      assignment.username?.toLowerCase().includes(query) ||
      assignment.email?.toLowerCase().includes(query) ||
      assignment.group_name?.toLowerCase().includes(query) ||
      assignment.group_description?.toLowerCase().includes(query)
    );
  }));

  // Pagination
  let paginatedAssignments = $derived(filteredAssignments.slice(
    (currentPage - 1) * itemsPerPage,
    currentPage * itemsPerPage
  ));

  // Reset to page 1 when search changes
  let prevSearchQuery = $state('');

  const roleSummaryRows = $derived(
    defaultRoleOrder
      .map((roleName) => {
        const role = roles.find((r) => r.name === roleName);
        if (!role) return null;
        const roleMembers = members.filter((m) => m.roles.some((r) => r.role_name === roleName));
        const roleGroups = groupAssignments.filter((g) => g.roles.some((r) => r.role_name === roleName));
        return { name: roleName, role, members: roleMembers, groups: roleGroups, access: getEffectiveAccess(roleName, roleMembers) };
      })
      .filter(Boolean)
  );

  const roleSummaryColumns = [
    { key: 'name', label: 'Role', slot: 'role' },
    { key: 'access', label: 'Effective Access', slot: 'access' },
    { key: 'members', label: 'Members', slot: 'members' },
  ];
  $effect(() => {
    if (searchQuery !== prevSearchQuery) {
      prevSearchQuery = searchQuery;
      if (searchQuery) {
        currentPage = 1;
      }
    }
  });

  // Event handlers for pagination
  function handlePageChange(event) {
    currentPage = event.detail.page;
    itemsPerPage = event.detail.itemsPerPage;
  }

  function handlePageSizeChange(event) {
    itemsPerPage = event.detail.itemsPerPage;
    currentPage = 1;
  }
</script>

<div class="space-y-6">
  <!-- Role Summary -->
  <div class="space-y-4 mb-8">
    <div class="flex items-start gap-3">
      <Shield class="w-4 h-4 text-blue-600 mt-0.5" />
      <div>
        <h3 class="text-sm font-semibold" style="color: var(--ds-text);">Summary</h3>
        <p class="text-sm mt-1" style="color: var(--ds-text-subtle);">
          Shows effective role assignments for this workspace. Roles without explicit members are open to everyone. Hierarchy: Viewer &rarr; Editor &rarr; Tester.
        </p>
      </div>
    </div>

    <DataTable columns={roleSummaryColumns} data={roleSummaryRows} keyField="name">
      {#snippet role(row)}
        <div class="font-medium" style="color: var(--ds-text);">{row.name}</div>
        <DescriptionText as="div">{row.role.description}</DescriptionText>
      {/snippet}
      {#snippet access(row)}
        {#if row.access.type === 'members'}
          <Chip color="blue">{row.access.count} {row.access.count === 1 ? 'member' : 'members'}</Chip>
        {:else if row.access.type === 'everyone'}
          <Chip color="green">Everyone</Chip>
        {:else}
          <span class="text-xs" style="color: var(--ds-text-subtle);">&mdash;</span>
        {/if}
      {/snippet}
      {#snippet members(row)}
        {#if row.members.length > 0 || row.groups.length > 0}
          <div class="flex flex-wrap gap-2">
            {#each row.members as m}
              <Chip color="blue">{m.first_name} {m.last_name}</Chip>
            {/each}
            {#each row.groups as g}
              <Chip color="purple" icon={Users}>{g.group_name}</Chip>
            {/each}
          </div>
        {:else}
          <span class="text-xs" style="color: var(--ds-text-subtle);">No direct members</span>
        {/if}
      {/snippet}
    </DataTable>
  </div>

  <!-- Assignment actions -->
  <div class="flex items-center justify-end gap-2">
    <Button variant="primary" size="medium" onclick={() => showModal = true} keyboardHint="A" hotkeyConfig={{ key: toHotkeyString('workspaceMembers', 'addMember'), guard: () => !showModal }}>
      <UserPlus class="w-4 h-4 mr-2" />
      Add Member
    </Button>
    <Button
      variant="default"
      size="medium"
      onclick={openGroupModal}
      keyboardHint="G"
      hotkeyConfig={{ key: toHotkeyString('workspaceMembers', 'addGroup'), guard: canOpenGroupModal }}
    >
      <Users class="w-4 h-4 mr-2" />
      Add Group
    </Button>
  </div>

  <!-- Search Box -->
  <SearchInput
    bind:value={searchQuery}
    placeholder="Search members or groups by name or email..."
  />

  <!-- Members Table -->
  {#if loading}
    <div class="text-center py-12" style="color: var(--ds-text-subtle);">
      Loading workspace members...
    </div>
  {:else if error}
    <div class="text-center py-12 text-red-600">
      {error}
    </div>
  {:else}
    <div data-testid="workspace-member-assignments">
      <DataTable
        {columns}
        data={paginatedAssignments}
        keyField="row_key"
        emptyMessage="No members or groups yet. Add users or groups to this workspace to grant access."
        emptyIcon={Shield}
        actionItems={getAssignmentActionItems}
        rowAttrs={(item) => ({ 'data-testid': `workspace-member-${item.row_key}` })}
      >
      {#snippet principal(item)}
        <div class="flex items-center gap-3">
          {#if item.type === 'group'}
            <div class="flex items-center justify-center w-8 h-8 rounded-full" style="background-color: var(--ds-background-accent-purple-subtler);">
              <Users class="w-4 h-4" style="color: var(--ds-accent-purple);" />
            </div>
          {:else}
            <Avatar
              src={item.avatar_url}
              firstName={item.first_name}
              lastName={item.last_name}
              size="sm"
              variant="blue"
            />
          {/if}
          <div>
            <div class="flex items-center gap-2">
              <Text size="sm" weight="medium">{item.displayName}</Text>
              {#if item.type === 'group'}
                <Chip color="purple">Group</Chip>
              {/if}
            </div>
            <Text size="xs" variant="subtle">{item.detail}</Text>
          </div>
        </div>
      {/snippet}

      {#snippet role(item)}
        <div class="flex flex-wrap gap-2">
          {#each item.roles as role}
            <span class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium" style={getRoleBadgeStyle(role.role_id)}>
              <Shield class="w-3 h-3" />
              {role.role_name}
            </span>
          {/each}
        </div>
      {/snippet}
      </DataTable>
    </div>

    {#if filteredAssignments.length > 0}
      <Pagination
        currentPage={currentPage}
        totalItems={filteredAssignments.length}
        itemsPerPage={itemsPerPage}
        pageSizeOptions={[10, 20, 50]}
        onpageChange={handlePageChange}
        onpageSizeChange={handlePageSizeChange}
      />
    {:else if searchQuery.trim()}
      <div class="text-sm text-center py-4" style="color: var(--ds-text-subtle);">
        No members or groups found matching "{searchQuery}"
      </div>
    {/if}
  {/if}
</div>

<!-- Add Member Modal -->
<Modal
  isOpen={showModal}
  onSubmit={handleSubmit}
  submitDisabled={!selectedUserId || !selectedRoleId || adding}
  maxWidth="max-w-2xl"
  onclose={handleCancel}
>
  {#snippet children(submitHint)}
  <div class="p-6">
    <h2 class="text-xl font-semibold mb-6" style="color: var(--ds-text);">
      Add Workspace Member
    </h2>

    <div class="space-y-4">
      <div>
        <Label color="default" required class="mb-2">User</Label>
        <UserPicker bind:value={selectedUserId} placeholder="Select user..." />
      </div>

      <div>
        <Label color="default" required class="mb-2">Role</Label>
        <Select
          bind:value={selectedRoleId}
          onchange={(value) => selectedRoleId = value ? Number(value) : null}
          options={[{ value: null, label: 'Select role...' }, ...roles.map(role => ({ value: role.id, label: `${role.name} - ${role.description}` }))]}
        />
      </div>
    </div>

    <div class="mt-8 flex gap-3">
      <Button
        variant="primary"
        size="medium"
        onclick={handleSubmit}
        disabled={!selectedUserId || !selectedRoleId || adding}
        keyboardHint={submitHint}
      >
        {adding ? 'Adding...' : 'Add Member'}
      </Button>
      <Button
        variant="default"
        size="medium"
        onclick={handleCancel}
        disabled={adding}
        keyboardHint="Esc"
      >
        Cancel
      </Button>
    </div>
  </div>
  {/snippet}
</Modal>

<!-- Add Group Modal -->
<Modal
  isOpen={showGroupModal}
  onSubmit={handleSubmitGroup}
  submitDisabled={!selectedGroupId || !selectedGroupRoleId || addingGroup}
  maxWidth="max-w-2xl"
  onclose={handleCancelGroup}
>
  {#snippet children(submitHint)}
  <div class="p-6">
    <h2 class="text-xl font-semibold mb-6" style="color: var(--ds-text);">
      Add Group to Workspace
    </h2>

    <div class="space-y-4">
      <div>
        <Label color="default" required class="mb-2">Group</Label>
        <GroupPicker bind:value={selectedGroupId} placeholder="Select group..." />
      </div>

      <div>
        <Label color="default" required class="mb-2">Role</Label>
        <Select
          bind:value={selectedGroupRoleId}
          onchange={(value) => selectedGroupRoleId = value ? Number(value) : null}
          options={[{ value: null, label: 'Select role...' }, ...roles.map(role => ({ value: role.id, label: `${role.name} - ${role.description}` }))]}
        />
      </div>
    </div>

    <div class="mt-8 flex gap-3">
      <Button
        variant="primary"
        size="medium"
        onclick={handleSubmitGroup}
        disabled={!selectedGroupId || !selectedGroupRoleId || addingGroup}
        keyboardHint={submitHint}
      >
        {addingGroup ? 'Adding...' : 'Add Group'}
      </Button>
      <Button
        variant="default"
        size="medium"
        onclick={handleCancelGroup}
        disabled={addingGroup}
        keyboardHint="Esc"
      >
        Cancel
      </Button>
    </div>
  </div>
  {/snippet}
</Modal>
