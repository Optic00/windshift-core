<script>
  import { currentRoute } from '../router.js';
  import { workspacePermissions } from '../stores';
  import { workspaceSettingsItems, workspaceSettingsRoute } from '../navigation/workspaceNavigation.js';
  import { t } from '../stores/i18n.svelte.js';

  let { workspaceId = null } = $props();

  const canAdmin = $derived(workspacePermissions.canAdminWorkspace(workspaceId));

  // Mirror WorkspaceNavigation's navLink styling so the admin nav is visually
  // identical to the rest of the workspace sidebar.
  function navItemStyle(isActive, danger) {
    const color = danger ? 'var(--ds-text-danger)' : isActive ? 'var(--ds-text)' : 'var(--ds-text-subtle)';
    return isActive && !danger
      ? `background: var(--ds-surface-selected); color: ${color};`
      : `color: ${color};`;
  }

  function onMouseEnter(event, isActive, danger) {
    if (!isActive) {
      const color = danger ? 'var(--ds-text-danger)' : 'var(--ds-text)';
      event.currentTarget.style.cssText = `background: var(--ds-background-neutral-hovered); color: ${color};`;
    }
  }

  function onMouseLeave(event, isActive, danger) {
    event.currentTarget.style.cssText = navItemStyle(isActive, danger);
  }
</script>

{#if canAdmin}
  <nav class="flex-1 px-4 space-y-2 overflow-y-auto" data-testid="workspace-admin-nav" aria-label={t('workspaceSettings.title')}>
    {#each workspaceSettingsItems as item (item.id)}
      {@const ItemIcon = item.icon}
      {@const active = $currentRoute.view === item.view}
      <a
        href={workspaceSettingsRoute(workspaceId, item.id)}
        class="w-full text-left cursor-pointer px-3 py-2 rounded-lg text-sm font-medium flex items-center gap-2 workspace-nav-item no-underline"
        style={navItemStyle(active, item.danger)}
        data-testid="workspace-admin-nav-{item.id}"
        aria-current={active ? 'page' : undefined}
        onmouseenter={(e) => onMouseEnter(e, active, item.danger)}
        onmouseleave={(e) => onMouseLeave(e, active, item.danger)}
      >
        <ItemIcon class="w-4 h-4" />
        {t(item.labelKey)}
      </a>
    {/each}
  </nav>
{/if}
