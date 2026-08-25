// ProseMirror plugin that sanitizes link hrefs and image srcs to prevent XSS
// from javascript:, vbscript:, and data: URL schemes rendered via Markdown.

import { Plugin, PluginKey } from '@milkdown/kit/prose/state';
import { $prose } from '@milkdown/kit/utils';
import { isSafeMarkdownURL } from '../utils/markdown-url-policy.ts';

function isEmptyUrl(url) {
  if (!url) return true;
  return url.trim() === '';
}

/** @param {string} url */
export function isSafeUrl(url) {
  return isEmptyUrl(url) || isSafeMarkdownURL(url);
}

/** @param {string} url */
export function isSafeImageUrl(url) {
  return isEmptyUrl(url) || isSafeMarkdownURL(url, { image: true, allowBlobImage: true });
}

const linkSanitizerPluginKey = new PluginKey('link-sanitizer');

/**
 * Milkdown plugin that sanitizes all link hrefs and image srcs on every
 * document change (including initial load). This is a belt-and-suspenders
 * defense against malicious Markdown stored in the database.
 */
export const linkSanitizerPlugin = $prose(() => {
  /**
   * Walk the document and build a transaction that replaces any unsafe
   * link hrefs or image srcs with '#unsafe-link-removed'.
   */
  function sanitizeDoc(tr) {
    let changed = false;
    tr.doc.descendants((node, pos) => {
      // Sanitize link marks on text nodes
      if (node.marks) {
        node.marks.forEach((mark) => {
          if (mark.type.name === 'link' && mark.attrs.href && !isSafeUrl(mark.attrs.href)) {
            tr.removeMark(pos, pos + node.nodeSize, mark.type);
            tr.addMark(
              pos,
              pos + node.nodeSize,
              mark.type.create({ ...mark.attrs, href: '#unsafe-link-removed' })
            );
            changed = true;
          }
        });
      }
      // Sanitize image nodes
      if (node.type.name === 'image' && node.attrs.src && !isSafeImageUrl(node.attrs.src)) {
        tr.setNodeMarkup(pos, undefined, { ...node.attrs, src: '#unsafe-link-removed' });
        changed = true;
      }
    });
    return changed;
  }

  return new Plugin({
    key: linkSanitizerPluginKey,

    // Intercept every transaction to sanitize any new or changed links
    appendTransaction(_transactions, _oldState, newState) {
      const tr = newState.tr;
      const changed = sanitizeDoc(tr);
      return changed ? tr : null;
    },

    props: {
      // Belt-and-suspenders: block click navigation to unsafe URLs
      handleClick(_view, _pos, event) {
        const link = /** @type {HTMLElement | null} */ (event.target)?.closest?.('a[href]');
        if (link && !isSafeUrl(link.getAttribute('href'))) {
          event.preventDefault();
          event.stopPropagation();
          return true;
        }
        return false;
      },
    },
  });
});
