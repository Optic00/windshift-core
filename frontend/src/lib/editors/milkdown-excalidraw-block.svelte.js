import { $node as defineNode, $remark as defineRemark, $view as defineView } from '@milkdown/utils';
import { mount, unmount } from 'svelte';
import ExcalidrawBlockView from './ExcalidrawBlockView.svelte';
import MermaidBlockView from './MermaidBlockView.svelte';

// Round-trips a fenced code block with language `excalidraw`. The block body
// is a single-line JSON object: {"attachmentId":N,"name":"..."}. Storing only
// the attachment ID keeps per-page revisions tiny and lets the diagram live
// in the existing attachments table.
//
// Milkdown's parser picks the first NodeSchema whose parseMarkdown.match()
// returns true, and the schema is iterated in registration order — commonmark
// registers code_block before us, so a naive match on `{type:'code', lang}`
// would lose to the generic code_block. To win cleanly, a tiny remark
// transformer rewrites matching `code` nodes to `type:'excalidraw'` before
// the schema-match step; on the way back out, toMarkdown emits a normal
// `code` node so remark stringifies it as a standard fence.

const excalidrawRemark = defineRemark('excalidraw-fence', () => () => (tree) => {
  visit(tree, (node) => {
    if (node && node.type === 'code' && node.lang === 'excalidraw') {
      let parsed = {};
      try {
        parsed = JSON.parse(node.value || '{}');
      } catch {
        parsed = {};
      }
      node.type = 'excalidraw';
      node.attachmentId = Number.isInteger(parsed.attachmentId) ? parsed.attachmentId : null;
      node.name = typeof parsed.name === 'string' ? parsed.name : '';
      delete node.lang;
      delete node.meta;
      delete node.value;
    }
  });
});

function visit(node, fn) {
  if (!node) return;
  fn(node);
  if (Array.isArray(node.children)) {
    for (const child of node.children) visit(child, fn);
  }
}

export const excalidrawNode = defineNode('excalidraw', () => ({
  group: 'block',
  atom: true,
  isolating: true,
  selectable: true,
  draggable: false,
  attrs: {
    attachmentId: { default: null },
    name: { default: '' },
  },
  parseDOM: [
    {
      tag: 'div[data-excalidraw-block]',
      getAttrs: (dom) => ({
        attachmentId: Number(dom.getAttribute('data-attachment-id')) || null,
        name: dom.getAttribute('data-name') || '',
      }),
    },
  ],
  toDOM: (node) => [
    'div',
    {
      'data-excalidraw-block': '',
      'data-attachment-id': node.attrs.attachmentId ?? '',
      'data-name': node.attrs.name ?? '',
    },
  ],
  parseMarkdown: {
    // Matches only the rewritten mdast nodes produced by `excalidrawRemark`,
    // so we never clash with commonmark's code_block runner.
    match: (node) => node.type === 'excalidraw',
    runner: (state, node, type) => {
      state.addNode(type, {
        attachmentId: Number.isInteger(node.attachmentId) ? node.attachmentId : null,
        name: typeof node.name === 'string' ? node.name : '',
      });
    },
  },
  toMarkdown: {
    match: (node) => node.type.name === 'excalidraw',
    runner: (state, node) => {
      const payload = JSON.stringify({
        attachmentId: node.attrs.attachmentId,
        name: node.attrs.name,
      });
      state.addNode('code', undefined, payload, { lang: 'excalidraw' });
    },
  },
}));

export const excalidrawView = defineView(excalidrawNode, () => {
  return (node, view, getPos) => {
    const dom = document.createElement('div');
    dom.className = 'milkdown-excalidraw-block';
    dom.setAttribute('data-excalidraw-block', '');

    const props = $state({
      attachmentId: node.attrs.attachmentId,
      name: node.attrs.name,
      readonly: !view.editable,
      onEdit: () => {
        dom.dispatchEvent(
          new CustomEvent('excalidraw:edit', {
            bubbles: true,
            detail: {
              attachmentId: props.attachmentId,
              name: props.name,
              getPos,
            },
          })
        );
      },
    });

    const app = mount(ExcalidrawBlockView, { target: dom, props });

    return {
      dom,
      update(updated) {
        if (updated.type.name !== 'excalidraw') return false;
        props.attachmentId = updated.attrs.attachmentId;
        props.name = updated.attrs.name;
        props.readonly = !view.editable;
        return true;
      },
      stopEvent: () => true,
      ignoreMutations: () => true,
      destroy() {
        try {
          unmount(app);
        } catch (_e) {
          // Mount may already be torn down by the editor; ignore.
        }
      },
    };
  };
});

// ─── Mermaid fence block ──────────────────────────────────────────────
// Same architecture as the excalidraw block above — a remark transformer
// rewrites `code lang=mermaid` mdast nodes to a custom `mermaid` type so
// commonmark's code_block runner never claims them, then the schema's
// parseMarkdown.match keys off the new type. Read-only: the source lives
// inline in the markdown and round-trips back out as a standard mermaid
// fence. Rendering happens client-side via mermaid.js, mounted from a
// Svelte node view that defers the (~180KB) mermaid import to first use.

const mermaidRemark = defineRemark('mermaid-fence', () => () => (tree) => {
  visit(tree, (node) => {
    if (node && node.type === 'code' && node.lang === 'mermaid') {
      const source = node.value || '';
      node.type = 'mermaid';
      node.source = source;
      delete node.lang;
      delete node.meta;
      delete node.value;
    }
  });
});

export const mermaidNode = defineNode('mermaid', () => ({
  group: 'block',
  atom: true,
  isolating: true,
  selectable: true,
  draggable: false,
  attrs: { source: { default: '' } },
  parseDOM: [
    {
      tag: 'div[data-mermaid-block]',
      getAttrs: (dom) => ({
        source: dom.getAttribute('data-source') || '',
      }),
    },
  ],
  toDOM: (node) => ['div', { 'data-mermaid-block': '', 'data-source': node.attrs.source ?? '' }],
  parseMarkdown: {
    match: (node) => node.type === 'mermaid',
    runner: (state, node, type) => {
      state.addNode(type, {
        source: typeof node.source === 'string' ? node.source : '',
      });
    },
  },
  toMarkdown: {
    match: (node) => node.type.name === 'mermaid',
    runner: (state, node) => {
      state.addNode('code', undefined, node.attrs.source || '', { lang: 'mermaid' });
    },
  },
}));

export const mermaidView = defineView(mermaidNode, () => {
  return (node, view) => {
    const dom = document.createElement('div');
    dom.className = 'milkdown-mermaid-block';
    dom.setAttribute('data-mermaid-block', '');

    const props = $state({
      source: node.attrs.source,
      readonly: !view.editable,
    });

    const app = mount(MermaidBlockView, { target: dom, props });

    return {
      dom,
      update(updated) {
        if (updated.type.name !== 'mermaid') return false;
        props.source = updated.attrs.source;
        props.readonly = !view.editable;
        return true;
      },
      stopEvent: () => true,
      ignoreMutations: () => true,
      destroy() {
        try {
          unmount(app);
        } catch (_e) {
          // Mount may already be torn down by the editor; ignore.
        }
      },
    };
  };
});

export const excalidrawBlock = [
  excalidrawRemark,
  excalidrawNode,
  excalidrawView,
  mermaidRemark,
  mermaidNode,
  mermaidView,
].flat();
