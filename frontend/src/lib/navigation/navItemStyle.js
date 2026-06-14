// Shared styling for workspace sidebar nav items.
//
// Every workspace sidebar mode (regular workspace, personal, admin drilldown)
// renders the same item chrome — selected/hover background, subtle vs. primary
// text, and an optional `danger` variant. Keeping it here means the modes stay
// visually identical instead of each carrying its own copy of the logic.

export function navItemStyle(isActive, danger = false) {
  const color = danger
    ? 'var(--ds-text-danger)'
    : isActive
      ? 'var(--ds-text)'
      : 'var(--ds-text-subtle)';
  return isActive && !danger
    ? `background: var(--ds-surface-selected); color: ${color};`
    : `color: ${color};`;
}

export function onNavMouseEnter(event, isActive, danger = false) {
  if (isActive) return;
  const color = danger ? 'var(--ds-text-danger)' : 'var(--ds-text)';
  event.currentTarget.style.cssText = `background: var(--ds-background-neutral-hovered); color: ${color};`;
}

export function onNavMouseLeave(event, isActive, danger = false) {
  event.currentTarget.style.cssText = navItemStyle(isActive, danger);
}
