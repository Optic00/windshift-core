import { formatAuthenticatedInstant } from './authenticatedDateFormatter.js';
import { escapeHtml } from './sanitize.ts';
import { getStatusInlineStyle } from './statusColors.js';

/** Build the shared Collections/Search table columns with page-specific URL,
 * trailing date column, and status styling inputs. */
export function buildWorkItemColumns({ itemUrl, lastColumn, allStatuses, statusCategories }) {
  return [
    {
      key: 'display_key',
      label: 'Key',
      width: 'w-28',
      html: true,
      render: (item) =>
        `<a href="${itemUrl(item)}" class="text-xs font-mono px-1.5 py-0.5 rounded whitespace-nowrap no-underline" style="color: var(--ds-text-subtle); background-color: var(--ds-interactive-subtle);">${escapeHtml(item.display_key)}</a>`,
    },
    {
      key: 'title',
      label: 'Title',
      html: true,
      render: (item) =>
        `<a href="${itemUrl(item)}" class="block truncate text-sm no-underline" style="color: inherit;" title="${escapeHtml(item.title)}">${escapeHtml(item.title) || '—'}</a>`,
    },
    {
      key: 'workspace_name',
      label: 'Workspace',
      width: 'w-36',
      html: true,
      render: (item) =>
        `<span class="block truncate" title="${escapeHtml(item.workspace_name)}">${escapeHtml(item.workspace_name) || '—'}</span>`,
    },
    {
      key: 'status_name',
      label: 'Status',
      width: 'w-28',
      html: true,
      render: (item) =>
        item.status_name
          ? `<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium whitespace-nowrap" style="${getStatusInlineStyle(item.status_name, allStatuses, statusCategories)}">${escapeHtml(item.status_name)}</span>`
          : '—',
    },
    {
      key: 'priority_name',
      label: 'Priority',
      width: 'w-24',
      html: true,
      render: (item) =>
        item.priority_name
          ? `<span class="text-sm font-medium capitalize whitespace-nowrap" style="color: ${escapeHtml(item.priority_color) || 'var(--ds-text-subtle)'}">${escapeHtml(item.priority_name)}</span>`
          : '—',
    },
    lastColumn,
    { key: 'actions', label: '', width: 'w-12' },
  ];
}

/** Default Created column for Collections. */
export function createdAtColumn() {
  return {
    key: 'created_at',
    label: 'Created',
    width: 'w-28',
    html: true,
    render: (item) =>
      `<span class="whitespace-nowrap">${formatAuthenticatedInstant(item.created_at, { year: 'numeric', month: '2-digit', day: '2-digit' }) || '—'}</span>`,
  };
}

/** Updated column with a caller-supplied label for SearchPage. */
export function updatedAtColumn(label) {
  return {
    key: 'updated_at',
    label,
    width: 'w-28',
    html: true,
    render: (item) =>
      `<span class="whitespace-nowrap">${formatAuthenticatedInstant(item.updated_at, { year: 'numeric', month: '2-digit', day: '2-digit' }) || '—'}</span>`,
  };
}
