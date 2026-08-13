import { $node as defineNode, $remark as defineRemark, $view as defineView } from '@milkdown/utils';
import { mount, unmount } from 'svelte';
import ExcalidrawBlockView from './ExcalidrawBlockView.svelte';
import MermaidBlockView from './MermaidBlockView.svelte';

// Round-trip `excalidraw` fences as attachment-ID JSON. A remark transform
// rewrites matching code nodes before commonmark claims them; serialization
// restores standard fences.

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
    // Match only transformed nodes, never commonmark code blocks.
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
      onDelete: () => {
        dom.dispatchEvent(
          new CustomEvent('excalidraw:delete', {
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

// Mermaid fences are transformed before commonmark claims them, round-trip as
// inline source, and lazy-load Mermaid in the client node view.

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
