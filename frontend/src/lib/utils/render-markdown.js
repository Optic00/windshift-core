import DOMPurify from 'dompurify';
import { marked } from 'marked';

marked.setOptions({
  gfm: true,
  breaks: false,
  // pedantic / mangle / headerIds aren't options on this marked version; rely on defaults.
});

/**
 * Render a Markdown string to sanitized HTML.
 *
 * Intended for short Markdown blurbs in OpenAPI `description` fields and
 * similar non-editable contexts. For full editing use Milkdown.
 */
export function renderMarkdown(input) {
  if (!input) return '';
  const raw = marked.parse(String(input), { async: false });
  return DOMPurify.sanitize(raw, { USE_PROFILES: { html: true } });
}
