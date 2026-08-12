import { toExternal } from '../../runtime/contextPath.js';

const PAYLOAD_PREFIX = 'windshift-markdown-print:';

export function openMarkdownPrintView(path, kind, payload) {
  const printWindow = window.open('', '_blank');
  if (!printWindow) return false;

  printWindow.name = `${PAYLOAD_PREFIX}${JSON.stringify({ kind, ...payload })}`;
  printWindow.opener = null;
  printWindow.location.replace(toExternal(path));
  return true;
}

export function readMarkdownPrintPayload(expectedKind) {
  const encoded = window.name;
  window.name = '';
  if (!encoded.startsWith(PAYLOAD_PREFIX)) return null;

  try {
    const payload = JSON.parse(encoded.slice(PAYLOAD_PREFIX.length));
    if (
      payload?.kind !== expectedKind ||
      typeof payload.title !== 'string' ||
      typeof payload.content !== 'string'
    ) {
      return null;
    }
    return payload;
  } catch {
    return null;
  }
}
