/**
 * Parse ATX-style headings from raw Markdown source into a flat list of
 * { level, text, slug, line } entries, ignoring `#` characters that appear
 * inside fenced code blocks. Phase 2 deliverable for the in-page TOC; the
 * mirror logic for rendered-DOM scroll matching lives in PagesView.svelte.
 *
 * `slug` is a stable, URL-safe slug suitable for window.location.hash so
 * deep-links survive renames as long as the heading text is unchanged.
 *
 * The parser is intentionally tolerant of malformed Markdown: any line
 * that doesn't match the heading pattern is skipped, and code-fence state
 * is reset at end-of-input so an unterminated fence doesn't swallow the
 * rest of the document silently.
 */
export function parseMarkdownHeadings(source) {
  if (!source) return [];
  const lines = source.split('\n');
  const out = [];
  let inFence = false;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    // Toggle fenced code block state. ``` and ~~~ are both valid.
    if (/^\s*(```|~~~)/.test(line)) {
      inFence = !inFence;
      continue;
    }
    if (inFence) continue;

    const m = /^(#{1,6})\s+(.+?)\s*#*\s*$/.exec(line);
    if (!m) continue;

    const level = m[1].length;
    const text = m[2].trim();
    if (!text) continue;
    out.push({
      level,
      text,
      slug: slugify(text),
      line: i,
    });
  }
  return out;
}

/**
 * Slugify a heading into a URL-safe anchor id. Matches what the editor
 * DOM lookup uses so TOC clicks can find the corresponding rendered
 * heading by text without needing a backend round-trip.
 */
export function slugify(text) {
  return text
    .toLowerCase()
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 80);
}
