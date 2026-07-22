<script>
  import { CalendarDays, MoreHorizontal } from '@lucide/svelte';
  import Avatar from '../../components/Avatar.svelte';
  import Chip from '../../components/Chip.svelte';
  import DropIndicator from '../../layout/DropIndicator.svelte';
  import DropdownMenu from '../../layout/DropdownMenu.svelte';
  import { formatDateShort } from '../../utils/dateFormatter.js';
  import { itemTypeIconMap } from '../../utils/icons.js';
  import ItemKey from '../items/ItemKey.svelte';
  import CardFieldChip from './CardFieldChip.svelte';
  import DependencySummary from './DependencySummary.svelte';

  let {
    item,
    workspace = null,
    itemTypes = [],
    cardFields = [],
    priorities = [],
    statuses = [],
    iterations = [],
    projects = [],
    labels = [],
    customFieldDefinitions = [],
    users = [],
    dependencyLinks = [],
    moveMenuItems = [],
    closestEdge = null,
    swimlaneParentId = '',
    cardStyle = 'background-color: var(--ds-surface-raised); border-color: var(--ds-border);',
    textStyle = 'color: var(--ds-text);',
    onopen = null,
  } = $props();

  const resolvedMoveMenuItems = $derived(
    moveMenuItems.length > 0
      ? moveMenuItems
      : [{ id: `no-moves-${item.id}`, type: 'text', text: 'No available moves' }],
  );

  const bodyCardFields = $derived(
    cardFields.filter(
      (field) => !(field.field_type === 'system' && field.field_identifier === 'due_date'),
    ),
  );
  const itemType = $derived(
    item.item_type_id ? itemTypes.find((type) => type.id === item.item_type_id) : null,
  );
  const showsDueDate = $derived(
    Boolean(
      item.due_date &&
        cardFields.some(
          (field) => field.field_type === 'system' && field.field_identifier === 'due_date',
        ),
    ),
  );
  const hasFooter = $derived(
    showsDueDate || Boolean(item.assignee_id) || dependencyLinks.length > 0,
  );

  const avatarVariants = ['blue', 'teal', 'purple', 'green', 'orange'];

  function avatarVariant(userId) {
    return avatarVariants[Math.abs(Number(userId) || 0) % avatarVariants.length];
  }

  function open(event) {
    onopen?.(item.id, event);
  }

  function handleKeydown(event) {
    if (event.key === 'Enter' || event.key === ' ') {
      open(event);
    }
  }
</script>

<div
  class="board-card relative rounded-[4px] border px-3 py-2.5"
  style={cardStyle}
  data-item-card
  data-item-id={item.id}
  data-swimlane-parent-id={swimlaneParentId}
  role="button"
  tabindex="0"
  onclick={open}
  onkeydown={handleKeydown}
>
  {#if closestEdge}
    <DropIndicator edge={closestEdge} />
  {/if}

  <div class="cursor-grab active:cursor-grabbing">
    <div class="min-w-0">
      <div class="flex min-h-5 items-center gap-1.5">
        <ItemKey
          {item}
          {workspace}
          size="compact"
          style="color: var(--ds-text-subtlest);"
        />
        <span class="flex-1"></span>
        <div class="board-card-menu -mr-1 shrink-0 transition-opacity">
          <DropdownMenu
            items={resolvedMoveMenuItems}
            placement="bottom-end"
            maxWidth="max-w-xs"
            triggerIcon={MoreHorizontal}
            triggerClass="p-1 rounded hover:bg-[var(--ds-background-neutral-hovered)] focus:ring-2 focus:ring-[var(--ds-border-focused)]"
            triggerStyle="color: var(--ds-text-subtle);"
            iconOnly
            showChevron={false}
            triggerTestid={`board-card-move-menu-${item.id}`}
          />
        </div>
      </div>

      <h4 class="mt-1 min-w-0 break-words text-sm font-medium leading-5" style={textStyle}>
        {item.title}
      </h4>

      {#if itemType || bodyCardFields.length > 0}
        <div class="mt-2 flex flex-wrap gap-1.5">
          {#if itemType}
            {@const TypeIcon = itemTypeIconMap[itemType.icon] || itemTypeIconMap.FileText}
            <Chip appearance="metadata" icon={TypeIcon} title="Item type">
              {itemType.name}
            </Chip>
          {/if}
          {#each bodyCardFields as cardField}
            <CardFieldChip
              {cardField}
              {item}
              {priorities}
              {statuses}
              {iterations}
              {projects}
              {labels}
              {customFieldDefinitions}
              {users}
            />
          {/each}
        </div>
      {/if}

      {#if hasFooter}
        <div
          class="mt-2.5 flex min-h-6 items-center gap-2 border-t pt-2"
          style="border-color: var(--ds-border);"
        >
          {#if showsDueDate}
            <span
              class="inline-flex items-center gap-1 text-[11px] leading-4"
              style="color: var(--ds-text-subtle);"
              title="Due date"
            >
              <CalendarDays class="h-3 w-3 shrink-0" />
              {formatDateShort(item.due_date)}
            </span>
          {/if}
          <DependencySummary {item} links={dependencyLinks} />
          <span class="flex-1"></span>
          {#if item.assignee_id}
            {@const assignee = users.find((user) => user.id === item.assignee_id)}
            {#if assignee}
              <Avatar
                src={assignee.avatar_url || item.assignee_avatar}
                firstName={assignee.first_name}
                lastName={assignee.last_name}
                size="2xs"
                variant={avatarVariant(assignee.id)}
                title="{assignee.first_name} {assignee.last_name}"
              />
            {/if}
          {/if}
        </div>
      {/if}
    </div>
  </div>
</div>

<style>
  .board-card {
    box-shadow: none;
    transition:
      border-color 140ms ease-in-out,
      background-color 140ms ease-in-out,
      box-shadow 140ms ease-in-out,
      opacity 200ms ease;
  }

  .board-card:hover {
    background-color: var(--ds-surface-raised-hovered) !important;
    border-color: var(--ds-border-bold) !important;
    box-shadow: var(--ds-shadow-raised);
  }

  .board-card:focus-visible {
    outline: 2px solid var(--ds-border-focused);
    outline-offset: 1px;
  }

  .board-card-menu {
    opacity: 0.35;
  }

  .board-card:hover .board-card-menu,
  .board-card:focus-within .board-card-menu {
    opacity: 1;
  }
</style>
